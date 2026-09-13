# Build standalone drivers into bin/.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw mgpbox unihedron ptpcam \
           polemaster sim

SDK_DRIVERS := asiccd asicaa

PI_DRIVERS := smpro-switch smpro-focuser asiair
PI_PACKAGES := smpro asiair

driverdir = $(if $(filter smpro-switch smpro-focuser,$(1)),smpro,$(1))

CHECK_PACKAGES := $(foreach d,$(DRIVERS) $(PI_PACKAGES),./$(d)/...) ./internal/contract/...

BIN := bin

.PHONY: build help all sdk pi deb deps alpacasim test test-sdk vet tidy clean install uninstall $(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS)

PREFIX ?= /usr/local
DESTDIR ?=
BINDIR := $(DESTDIR)$(PREFIX)/bin
DEVICESDIR := $(DESTDIR)/etc/alpacahurd/devices.d
REGISTRYFILE := $(DESTDIR)/etc/alpacahurd/drivers.conf

ALIAS_asiair := asiair-switch

build: $(DRIVERS) ## build drivers without vendor SDKs into ./bin (default)

help: ## list the targets
	@echo "goalpaca-devices — build the standalone Alpaca drivers."
	@echo
	@echo "Targets:"
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  make %-9s %s\n", $$1, $$2}'
	@echo "  make <name>   build one driver (e.g. make tenmicron)"
	@echo
	@echo "Drivers: $(DRIVERS)"
	@echo "Pi drivers (linux/arm64): $(PI_DRIVERS)"

all: build sdk pi ## build + the cgo ZWO-SDK drivers + the Pi drivers

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@CGO_ENABLED=1 go build -o $(BIN)/$@ ./$@/cmd/$@

sdk: $(SDK_DRIVERS) ## build the cgo ZWO-SDK drivers (needs libASICamera2 and libCAA)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@CGO_ENABLED=1 go build -o $(BIN)/$@ ./$@/cmd/$@

pi: $(PI_DRIVERS) ## cross-compile the Raspberry Pi drivers (linux/arm64) into ./bin/linux_arm64

$(PI_DRIVERS): | $(BIN)
	@echo "building $@ (linux/arm64)"
	@mkdir -p $(BIN)/linux_arm64
	@GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $(BIN)/linux_arm64/$@ ./$(call driverdir,$@)/cmd/$@

alpacasim: ## run goalpaca's one-of-every-type protocol sim (not guidable)
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

deb: ## build one .deb per driver into ./dist (amd64 + arm64)
	@build/build-deb

install: ## install drivers from ./bin into $(PREFIX)/bin and seed disabled entries
	@test -d $(BIN) || { echo "nothing built: run make first" >&2; exit 1; }
	@install -d $(BINDIR) $(DEVICESDIR)
	@for f in $(BIN)/*; do \
		[ -f "$$f" ] || continue; \
		n=$$(basename $$f); \
		install -m 0755 "$$f" "$(BINDIR)/$$n"; \
		echo "installed $(BINDIR)/$$n"; \
		sh build/register-driver "$(BINDIR)/$$n" "$(REGISTRYFILE)" "$(PREFIX)/bin/$$n" || exit 1; \
		for a in $(ALIAS_asiair); do \
			[ "$$n" = asiair ] || continue; \
			ln -sf "$$n" "$(BINDIR)/$$a"; \
			echo "  alias $(BINDIR)/$$a -> $$n"; \
		done; \
		e="$(DEVICESDIR)/$$n.json"; \
		if [ "$$n" != sim ] && [ ! -f "$$e" ]; then \
			if "$(BINDIR)/$$n" -schema commented > "$$e" 2>/dev/null; then \
				chmod 0644 "$$e"; \
				echo "  seeded $$e -- edit it and set \"enable\": true"; \
			else \
				rm -f "$$e"; \
			fi; \
		fi; \
	done

uninstall: ## remove installed drivers (device entries are kept)
	@for f in $(BIN)/*; do \
		[ -f "$$f" ] || continue; \
		n=$$(basename $$f); \
		sh build/register-driver "$(BINDIR)/$$n" "$(REGISTRYFILE)" "$(PREFIX)/bin/$$n" remove || exit 1; rm -f "$(BINDIR)/$$n"; \
		for a in $(ALIAS_asiair); do \
			if [ "$$n" = asiair ]; then rm -f "$(BINDIR)/$$a"; fi; \
		done; \
	done
	@echo "removed drivers from $(BINDIR); $(DEVICESDIR) left intact"

# Resolve only the committed module graph, independent of developer workspaces.
export GOWORK := off

test: ## run tests for drivers that do not require vendor SDKs
	go test $(CHECK_PACKAGES)

test-sdk: ## test vendor SDK drivers (requires installed ZWO libraries)
	go test ./asiccd/... ./asicaa/...

vet: ## vet SDK-free drivers, including Linux-only packages
	go vet $(CHECK_PACKAGES)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go vet $(CHECK_PACKAGES)

deps: ## download the versions recorded in go.mod
	go mod download

tidy: ## tidy the shared module dependencies
	go mod tidy

clean: ## remove ./bin, ./dist and any per-cmd build outputs
	@rm -rf $(BIN) dist
	@rm -f $(foreach d,$(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS),$(call driverdir,$(d))/cmd/$(d)/$(d))
