// Runtime probe executed inside the images by the image tests. Compiled with
// --release 8 so the same jar runs on every supported Java line; APIs newer
// than Java 8 are reached through reflection.
import java.awt.Font;
import java.awt.Graphics2D;
import java.awt.GraphicsEnvironment;
import java.awt.image.BufferedImage;
import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.lang.instrument.Instrumentation;
import java.net.HttpURLConnection;
import java.net.InetAddress;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.security.spec.ECGenParameterSpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.TimeZone;
import java.util.zip.ZipEntry;
import java.util.zip.ZipInputStream;
import java.util.zip.ZipOutputStream;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.spec.GCMParameterSpec;
import javax.imageio.ImageIO;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLException;

public class Probe {
    static int failures = 0;

    interface Check { String run() throws Exception; }

    static void check(String name, Check c) {
        try {
            String detail = c.run();
            System.out.println("PROBE " + name + " ok " + (detail == null ? "" : detail));
        } catch (Throwable t) {
            failures++;
            System.out.println("PROBE " + name + " FAIL " + t);
        }
    }

    static void require(boolean cond, String msg) {
        if (!cond) throw new IllegalStateException(msg);
    }

    static String status(String key) throws Exception {
        for (String line : Files.readAllLines(Paths.get("/proc/self/status"), StandardCharsets.UTF_8)) {
            if (line.startsWith(key + ":")) return line.substring(key.length() + 1).trim();
        }
        return "";
    }

    static int javaFeature() {
        String v = System.getProperty("java.specification.version");
        return v.startsWith("1.") ? Integer.parseInt(v.substring(2)) : Integer.parseInt(v);
    }

    static volatile boolean agentLoaded = false;

    public static void premain(String args, Instrumentation inst) {
        agentLoaded = inst != null;
        System.setProperty("ctyl.probe.agent", "loaded");
    }

    public static void main(String[] args) throws Exception {
        String mode = args.length > 0 ? args[0] : "check";
        if (mode.equals("wait")) { waitMode(); return; }
        if (mode.equals("agentcheck")) {
            check("javaagent", () -> { require("loaded".equals(System.getProperty("ctyl.probe.agent")), "premain did not run"); return null; });
            System.exit(failures == 0 ? 0 : 1);
        }
        boolean network = !"0".equals(System.getenv("PROBE_NETWORK"));
        boolean readOnlyRoot = "1".equals(System.getenv("PROBE_READONLY_ROOT"));

        check("version", () -> System.getProperty("java.runtime.version") + " " + System.getProperty("java.vendor"));
        check("identity", () -> {
            String uid = status("Uid").split("\\s+")[1];
            require(!uid.equals("0"), "running as root");
            return "uid=" + uid + " user.name=" + System.getProperty("user.name") + " user.home=" + System.getProperty("user.home");
        });
        check("capabilities", () -> {
            String eff = status("CapEff");
            require(eff.matches("0+"), "effective capabilities " + eff);
            return "CapEff=" + eff + " NoNewPrivs=" + status("NoNewPrivs");
        });
        check("pid1", () -> {
            String stat = new String(Files.readAllBytes(Paths.get("/proc/self/stat")), StandardCharsets.UTF_8);
            require(stat.startsWith("1 "), "java is not PID 1: " + stat.substring(0, 20));
            return null;
        });
        check("processes", () -> {
            List<String> pids = new ArrayList<>();
            for (File f : new File("/proc").listFiles()) if (f.getName().matches("\\d+")) pids.add(f.getName());
            require(pids.size() == 1 && pids.get(0).equals("1"), "unexpected processes " + pids);
            return null;
        });
        check("listeners", () -> {
            List<String> listening = new ArrayList<>();
            for (String file : new String[] {"/proc/net/tcp", "/proc/net/tcp6"}) {
                Path p = Paths.get(file);
                if (!Files.exists(p)) continue;
                for (String line : Files.readAllLines(p, StandardCharsets.UTF_8)) {
                    String[] f = line.trim().split("\\s+");
                    if (f.length > 3 && f[3].equals("0A")) listening.add(file + " " + f[1]);
                }
            }
            require(listening.isEmpty(), "listening sockets " + listening);
            return null;
        });
        check("signals", () -> {
            long ign = Long.parseLong(status("SigIgn"), 16);
            require((ign & (1L << 1)) == 0 && (ign & (1L << 14)) == 0, "SIGINT/SIGTERM ignored: " + status("SigIgn"));
            return "SigIgn=" + status("SigIgn");
        });
        check("timezone", () -> {
            String tz = System.getenv("TZ");
            require(tz == null || TimeZone.getDefault().getID().equals(tz), "default zone " + TimeZone.getDefault().getID() + " != TZ " + tz);
            return TimeZone.getDefault().getID();
        });
        check("encoding", () -> {
            String jnu = System.getProperty("sun.jnu.encoding");
            require("UTF-8".equalsIgnoreCase(jnu), "sun.jnu.encoding=" + jnu);
            return "file.encoding=" + System.getProperty("file.encoding");
        });
        check("unicode-filenames", () -> {
            Path p = Paths.get("wörld-世界-☃.txt");
            Files.write(p, "ok".getBytes(StandardCharsets.UTF_8));
            boolean listed = false;
            for (File f : new File(".").listFiles()) if (f.getName().equals(p.toString())) listed = true;
            require(listed, "unicode name not listed back");
            Files.delete(p);
            return null;
        });
        check("tmpdir", () -> {
            File f = File.createTempFile("probe", ".tmp");
            require(f.delete(), "cannot delete temp file");
            return System.getProperty("java.io.tmpdir");
        });
        if (readOnlyRoot) {
            check("readonly-root", () -> {
                try (FileOutputStream o = new FileOutputStream("/etc/ctyl-probe")) {
                    throw new IllegalStateException("root filesystem is writable");
                } catch (java.io.IOException expected) {
                    return "write to /etc rejected";
                }
            });
        }
        check("headless-awt-fonts", () -> {
            require(GraphicsEnvironment.isHeadless(), "not headless");
            String[] families = GraphicsEnvironment.getLocalGraphicsEnvironment().getAvailableFontFamilyNames();
            require(families.length > 0, "no fonts");
            BufferedImage img = new BufferedImage(128, 32, BufferedImage.TYPE_INT_ARGB);
            Graphics2D g = img.createGraphics();
            g.setFont(new Font(Font.SANS_SERIF, Font.PLAIN, 14));
            g.drawString("Minecraft ☃", 2, 20);
            g.dispose();
            ByteArrayOutputStream png = new ByteArrayOutputStream();
            require(ImageIO.write(img, "png", png) && png.size() > 0, "png encoding failed");
            return families.length + " font families";
        });
        check("crypto", () -> {
            KeyGenerator kg = KeyGenerator.getInstance("AES");
            kg.init(256);
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            byte[] iv = new byte[12];
            new SecureRandom().nextBytes(iv);
            c.init(Cipher.ENCRYPT_MODE, kg.generateKey(), new GCMParameterSpec(128, iv));
            c.doFinal("payload".getBytes(StandardCharsets.UTF_8));
            KeyPairGenerator ec = KeyPairGenerator.getInstance("EC");
            ec.initialize(new ECGenParameterSpec("secp256r1"));
            ec.generateKeyPair();
            KeyPairGenerator rsa = KeyPairGenerator.getInstance("RSA");
            rsa.initialize(2048);
            rsa.generateKeyPair();
            MessageDigest.getInstance("SHA-256").digest(new byte[16]);
            return null;
        });
        check("base64-zip", () -> {
            byte[] data = new byte[1 << 16];
            new SecureRandom().nextBytes(data);
            require(java.util.Arrays.equals(Base64.getDecoder().decode(Base64.getEncoder().encode(data)), data), "base64 roundtrip");
            ByteArrayOutputStream buf = new ByteArrayOutputStream();
            try (ZipOutputStream z = new ZipOutputStream(buf)) { z.putNextEntry(new ZipEntry("a")); z.write(data); }
            try (ZipInputStream z = new ZipInputStream(new java.io.ByteArrayInputStream(buf.toByteArray()))) {
                require(z.getNextEntry() != null, "zip entry");
            }
            return null;
        });
        check("process-spawn", () -> {
            String java = System.getProperty("java.home") + "/bin/java";
            Process p = new ProcessBuilder(java, "-version").redirectErrorStream(true).start();
            drain(p.getInputStream());
            require(p.waitFor() == 0, "child java failed");
            return "jspawnhelper ok";
        });
        check("unsafe", () -> {
            Class<?> u = Class.forName("sun.misc.Unsafe");
            java.lang.reflect.Field f = u.getDeclaredField("theUnsafe");
            f.setAccessible(true);
            require(f.get(null) != null, "no Unsafe");
            return null;
        });
        if (javaFeature() >= 9) {
            check("modules", () -> {
                Class<?> layerClass = Class.forName("java.lang.ModuleLayer");
                Object boot = layerClass.getMethod("boot").invoke(null);
                java.lang.reflect.Method find = layerClass.getMethod("findModule", String.class);
                String[] required = {"java.base", "java.desktop", "java.instrument", "java.logging", "java.management",
                    "java.naming", "java.net.http", "java.scripting", "java.sql", "java.xml", "jdk.unsupported",
                    "jdk.zipfs", "jdk.management", "jdk.jfr", "jdk.localedata", "jdk.charsets", "jdk.naming.dns",
                    "jdk.crypto.cryptoki", "jdk.httpserver", "jdk.net", "jdk.security.auth"};
                List<String> missing = new ArrayList<>();
                for (String m : required) if (!((java.util.Optional<?>) find.invoke(boot, m)).isPresent()) missing.add(m);
                require(missing.isEmpty(), "missing modules " + missing);
                String[] optional = {"jdk.attach", "jdk.compiler", "jdk.incubator.vector", "jdk.crypto.ec"};
                StringBuilder sb = new StringBuilder();
                for (String m : optional) sb.append(m).append(((java.util.Optional<?>) find.invoke(boot, m)).isPresent() ? "+ " : "- ");
                return sb.toString().trim();
            });
        }
        if (network) {
            check("dns", () -> InetAddress.getByName("sessionserver.mojang.com").getHostAddress());
            check("tls", () -> {
                HttpsURLConnection c = (HttpsURLConnection) new URL("https://piston-meta.mojang.com/mc/game/version_manifest_v2.json").openConnection();
                c.setConnectTimeout(15000);
                c.setReadTimeout(15000);
                int code = c.getResponseCode();
                require(code == 200, "HTTP " + code);
                return c.getCipherSuite();
            });
            check("tls-rejects-invalid-certificates", () -> {
                HttpURLConnection c = (HttpURLConnection) new URL("https://expired.badssl.com/").openConnection();
                c.setConnectTimeout(15000);
                try {
                    c.getResponseCode();
                } catch (SSLException expected) {
                    return "rejected: " + expected.getClass().getSimpleName();
                }
                throw new IllegalStateException("expired certificate accepted");
            });
        }
        System.out.println("PROBE-SUMMARY failures=" + failures);
        System.exit(failures == 0 ? 0 : 1);
    }

    static void drain(InputStream in) throws Exception {
        byte[] b = new byte[4096];
        while (in.read(b) >= 0) { }
    }

    static void waitMode() throws Exception {
        Runtime.getRuntime().addShutdownHook(new Thread(() -> {
            System.out.println("shutdown hook ran");
            System.out.flush();
        }));
        System.out.println("ready");
        System.out.flush();
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
        String line;
        while ((line = in.readLine()) != null) {
            line = line.trim();
            if (line.startsWith("exit ")) {
                System.exit(Integer.parseInt(line.substring(5)));
            }
            System.out.println("echo: " + line);
            System.out.flush();
        }
        Thread.sleep(Long.MAX_VALUE);
    }
}
