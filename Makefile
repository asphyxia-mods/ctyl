# Convenience targets; CI calls the scripts directly.
SHELL := /bin/bash
export PATH := $(CURDIR)/.tools/bin:$(PATH)
LINE ?= 21
ARCHS ?= x86_64,aarch64
ARCH ?= amd64

.PHONY: tools test fuzz image image-test minecraft regression probe

tools:
	scripts/install-tools.sh

test:
	test -z "$$(gofmt -l cmd internal tests)"
	go vet ./...
	go test -count=1 ./internal/... ./cmd/...
	shellcheck -x scripts/*.sh tests/security/*.sh

fuzz:
	go test -run XXX -fuzz FuzzParse -fuzztime 120s ./internal/startup/

image:
	scripts/build-line.sh $(LINE) --archs $(ARCHS)

build/probe/probe.jar: tests/probe/Probe.java
	mkdir -p build/probe/classes
	javac --release 8 -Xlint:-options -d build/probe/classes $<
	printf 'Main-Class: Probe\nPremain-Class: Probe\n' > build/probe/manifest.txt
	jar cfm $@ build/probe/manifest.txt -C build/probe/classes .

probe: build/probe/probe.jar

image-test: build/probe/probe.jar
	docker load -i build/java_$(LINE)/$$(source build/java_$(LINE)/selected.env && echo $$SELECTED)/image.tar
	CTYL_TEST_IMAGE=ctyl:java_$(LINE)-$(ARCH) CTYL_PROBE_JAR=$(CURDIR)/build/probe/probe.jar go test -count=1 -v ./tests/imagetest/

minecraft:
	CTYL_MC_TESTS=1 CTYL_MC_CACHE=$(CURDIR)/.cache/minecraft CTYL_MC_SERVERS='^java_$(LINE)/' \
	  CTYL_IMAGE_PATTERN='ctyl:java_%d-$(ARCH)' go test -count=1 -timeout 120m -v ./tests/minecraft/

regression:
	tests/security/regression.sh
