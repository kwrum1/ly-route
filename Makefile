SHELL := /bin/sh

ARCH ?= amd64
SUITE ?= bookworm
OUT ?= dist/rootfs

.PHONY: rootfs rootfs-amd64 rootfs-arm64 disk-image rockchip-armbian-image rockchip-boards runtime-debs validate-rootfs ci clean-rootfs

rootfs:
	./scripts/build-rootfs.sh --arch "$(ARCH)" --suite "$(SUITE)" --out "$(OUT)"

rootfs-amd64:
	./scripts/build-rootfs.sh --arch amd64 --suite "$(SUITE)" --out "$(OUT)"

rootfs-arm64:
	./scripts/build-rootfs.sh --arch arm64 --suite "$(SUITE)" --out "$(OUT)"

disk-image:
	./scripts/build-disk-image.sh --rootfs "$(ROOTFS)" --suite "$(SUITE)" --out "$(OUT)"

rockchip-armbian-image:
	./scripts/build-rockchip-armbian-image.sh --rootfs "$(ROOTFS)" --board "$(BOARD)" --suite "$(SUITE)" --out "$(OUT)"

rockchip-boards:
	./scripts/build-rockchip-armbian-image.sh --list-boards

runtime-debs:
	./scripts/build-runtime-debs.sh all

validate-rootfs:
	./scripts/validate-rootfs-scaffold.sh

ci:
	./scripts/ci-verify.sh

clean-rootfs:
	rm -rf "$(OUT)"
