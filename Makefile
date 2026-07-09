# goalpaca_devices — build the standalone Alpaca driver modules (each its own Go
# module) and the astrofleet aggregator into ./bin.
#
#   make              build every pure-Go driver + astrofleet into ./bin
#   make help         list the targets
#   make <name>       build one (e.g. make tenmicron, make astrofleet)
#   make sdk          build the cgo ZWO-SDK drivers (needs libASICamera2)
#   make all          build + sdk
#   make sim          run the Alpaca simulators (all device types, no hardware)
#   make vet          go vet every module
#   make tidy         go mod tidy every module
#   make clean        remove ./bin and any per-cmd build outputs
#   make install      build astrofleet + install the systemd service on the target
#                     (Raspberry Pi / Linux; needs root — sudo's the deploy script)

# Pure-Go drivers — build anywhere, no vendor SDK. astrocam is the RE'd ZWO camera
# driver (module asicam-alpaca) that replaced the SDK-based asicam.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw mgpbox unihedron

# cgo drivers that link the ZWO SDK (libASICamera2) — opt-in, not in the default build.
SDK_DRIVERS := asiccd asicaa

BIN := bin

.PHONY: build help install all sdk sim vet tidy clean astrofleet $(DRIVERS) $(SDK_DRIVERS)

build: $(DRIVERS) astrofleet ## build every pure-Go driver + astrofleet into ./bin (default)

# help: self-documenting — lists every target annotated with a "## " description below.
help: ## list the targets
	@echo "goalpaca-devices — build the Alpaca drivers and the astrofleet aggregator."
	@echo
	@echo "Targets:"
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  make %-9s %s\n", $$1, $$2}'
	@echo "  make <name>   build one driver (e.g. make tenmicron)"
	@echo
	@echo "Drivers: $(DRIVERS)"

all: build sdk ## build + the cgo ZWO-SDK drivers

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

# astrofleet: the vendor-free aggregator — one process, config-driven, also hosts the
# optional INDI server and LX200 bridge (see fleet/README.md).
astrofleet: | $(BIN) ## build just the astrofleet aggregator into ./bin
	@echo "building astrofleet"
	@cd fleet && CGO_ENABLED=1 go build -o ../$(BIN)/astrofleet .

# install: on-the-target deploy (Raspberry Pi / Linux), NOT a dev-box action. Builds
# astrofleet natively, then runs the deploy script as root: binary -> /usr/local/bin,
# starter config -> /etc/astrofleet, systemd unit + udev rules installed, service
# enabled and started. Run it on the machine the fleet runs on. See fleet/deploy/.
install: astrofleet ## install astrofleet as a systemd service on the target (needs root)
	@echo "installing astrofleet service (sudo prompts for the file/systemd steps)"
	@sudo fleet/deploy/install.sh $(BIN)/astrofleet

sdk: $(SDK_DRIVERS) ## build the cgo ZWO-SDK drivers (needs libASICamera2)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

# sim: run one of every simulated Alpaca device on a single port with discovery —
# the no-hardware testbed for client development (it is also a ConformU target).
sim: ## run the Alpaca simulators (all device types, no hardware)
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

vet: ## go vet every module
	@for d in $(DRIVERS) $(SDK_DRIVERS) fleet; do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done

tidy: ## go mod tidy every module
	@for d in $(DRIVERS) $(SDK_DRIVERS) fleet; do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean: ## remove ./bin and any per-cmd build outputs
	@rm -rf $(BIN)
	@rm -f $(foreach d,$(DRIVERS) $(SDK_DRIVERS),$(d)/cmd/$(d)/$(d)) fleet/astrofleet fleet/fleet
