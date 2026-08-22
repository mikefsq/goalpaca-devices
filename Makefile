# goalpaca_devices — build the standalone Alpaca driver modules (each its own Go
# module) into ./bin.
#
#   make              build every pure-Go driver into ./bin
#   make help         list the targets
#   make <name>       build one (e.g. make tenmicron)
#   make sdk          build the cgo ZWO-SDK drivers (needs libASICamera2)
#   make pi           cross-compile the Raspberry Pi drivers into ./bin/linux_arm64
#   make all          build + sdk + pi
#   make sim          build the coupled guide sim (mount + camera, one shared sky)
#   make alpacasim    run goalpaca's one-of-every-type protocol sim (not guidable)
#   make vet          go vet every module
#   make tidy         go mod tidy every module
#   make clean        remove ./bin and any per-cmd build outputs

# Pure-Go drivers — build anywhere, no vendor SDK. astrocam is the RE'd ZWO camera
# driver (module asicam-alpaca) that replaced the SDK-based asicam.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw mgpbox unihedron ptpcam sim

# cgo drivers that link the ZWO SDK (libASICamera2) — opt-in, not in the default build.
SDK_DRIVERS := asiccd asicaa

# Raspberry Pi drivers — the hardware exists only on a Pi (the SM Pro board's
# I2C/SPI/UART hats; asiair: the ASIAIR's own switch), so they cross-compile to
# linux/arm64 under bin/linux_arm64/ named for the driver they register, ready
# to copy to a Pi's /usr/local/bin where alpacahurd resolves drivers by name.
#
# The SM Pro is one module serving two ASCOM device types over hardware that
# shares nothing — the Switch has the I2C expander, DAC, ADC and dew PWM, the
# Focuser has the TMC2209 on its own UART — so it builds a binary per device and
# the two run as separate processes. PI_MODULES is the directories behind them,
# which is what a per-module command (vet, tidy) iterates.
PI_DRIVERS := smpro-switch smpro-focuser asiair
PI_MODULES := smpro asiair

# moduledir is the directory a driver is built from, which is the driver's own
# name unless a module produces several binaries.
moduledir = $(if $(filter smpro-switch smpro-focuser,$(1)),smpro,$(1))

BIN := bin

.PHONY: build help all sdk pi deb head alpacasim vet tidy clean $(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS)

build: $(DRIVERS) ## build every pure-Go driver into ./bin (default)

# help: self-documenting — lists every target annotated with a "## " description below.
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
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

sdk: $(SDK_DRIVERS) ## build the cgo ZWO-SDK drivers (needs libASICamera2)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

pi: $(PI_DRIVERS) ## cross-compile the Raspberry Pi drivers (linux/arm64) into ./bin/linux_arm64

$(PI_DRIVERS): | $(BIN)
	@echo "building $@ (linux/arm64)"
	@mkdir -p $(BIN)/linux_arm64
	@cd $(call moduledir,$@) && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o ../$(BIN)/linux_arm64/$@ ./cmd/$@

# sim (in DRIVERS above) builds bin/sim: the coupled mount + guide-camera pair on one
# shared simulated sky, so PHD2 can calibrate and guide a closed loop with no hardware.
# alpacasim runs goalpaca's uncoupled one-of-every-type sim — a ConformU/protocol
# target, not guidable (its camera frames don't respond to mount pulses).
alpacasim: ## run goalpaca's one-of-every-type protocol sim (not guidable)
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

# One .deb per driver, alpacahurd-<driver>, into ./dist. Each holds the driver
# binary and seeds a disabled entry in /etc/alpacahurd/devices.d/. build-deb
# builds from each module's committed go.mod, not from the workspace, so a
# driver that needs a sibling checkout fails here as it would in CI.
deb: ## build one .deb per driver into ./dist (amd64 + arm64)
	@build/build-deb

vet: ## go vet every module
	@for d in $(DRIVERS) $(SDK_DRIVERS); do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done
	@for d in $(PI_MODULES); do echo "vet $$d (linux/arm64)"; (cd $$d && GOOS=linux GOARCH=arm64 go vet ./...) || exit 1; done

# head repoints every github.com/mikefsq dependency at its branch head, so a
# plain clone builds what the sibling repos hold right now. Go has no way to
# say "track this branch" in a go.mod, and a pseudo-version is what it writes
# instead, so this is what tagging every library would otherwise buy: run it
# when a sibling gains a commit these modules need.
#
# asiair is skipped. It requires github.com/mikefsq/asiair, which is not a
# fetchable repository, and an unresolvable require blocks the module graph
# and with it `go get` itself.
head: ## repoint every mikefsq dependency at its branch head
	@for d in $(DRIVERS) $(SDK_DRIVERS) $(PI_MODULES); do \
		[ "$$d" = asiair ] && { echo "skip $$d (github.com/mikefsq/asiair is unfetchable)"; continue; }; \
		echo "head $$d"; \
		( cd $$d && \
		  for r in $$(grep -oE '^replace github.com/mikefsq/[a-z0-9./-]+' go.mod | awk '{print $$2}'); do \
			GOWORK=off go mod edit -dropreplace=$$r; \
		  done; \
		  for m in $$(grep -oE 'github.com/mikefsq/[a-z0-9./-]+' go.mod | grep -v goalpaca-devices | sort -u); do \
			GOWORK=off GOFLAGS=-mod=mod go get $$m@main > /dev/null 2>&1 || echo "  could not reach $$m@main"; \
		  done; \
		  GOWORK=off GOFLAGS=-mod=mod go mod download all > /dev/null 2>&1 ) || exit 1; \
	done
	@echo "run 'make deb' to confirm every module still builds from its go.mod"

tidy: ## go mod tidy every module
	@for d in $(DRIVERS) $(SDK_DRIVERS) $(PI_MODULES); do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean: ## remove ./bin, ./dist and any per-cmd build outputs
	@rm -rf $(BIN) dist
	@rm -f $(foreach d,$(DRIVERS) $(SDK_DRIVERS) $(PI_DRIVERS),$(call moduledir,$(d))/cmd/$(d)/$(d))
