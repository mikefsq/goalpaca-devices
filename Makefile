# goalpaca_devices — build the standalone Alpaca driver modules (each its own Go
# module) and the astrofleet aggregator into ./bin.
#
#   make              build every pure-Go driver + astrofleet into ./bin
#   make <name>       build one (e.g. make tenmicron, make astrofleet)
#   make sdk          build the cgo ZWO-SDK drivers (needs libASICamera2)
#   make all          build + sdk
#   make sim          run the Alpaca simulators (all device types, no hardware)
#   make vet          go vet every module
#   make tidy         go mod tidy every module
#   make clean        remove ./bin

# Pure-Go drivers — build anywhere, no vendor SDK. astrocam is the RE'd ZWO camera
# driver (module asicam-alpaca) that replaced the SDK-based asicam.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw

# cgo drivers that link the ZWO SDK (libASICamera2) — opt-in, not in the default build.
SDK_DRIVERS := asiccd asicaa

BIN := bin

.PHONY: build all sdk sim vet tidy clean astrofleet $(DRIVERS) $(SDK_DRIVERS)

build: $(DRIVERS) astrofleet

all: build sdk

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ .

# astrofleet: the vendor-free aggregator — one process, config-driven, also hosts the
# optional INDI server and LX200 bridge (see fleet/README.md).
astrofleet: | $(BIN)
	@echo "building astrofleet"
	@cd fleet && CGO_ENABLED=1 go build -o ../$(BIN)/astrofleet .

sdk: $(SDK_DRIVERS)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ .

# sim: run one of every simulated Alpaca device on a single port with discovery —
# the no-hardware testbed for client development (it is also a ConformU target).
sim:
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

vet:
	@for d in $(DRIVERS) $(SDK_DRIVERS) fleet; do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done

tidy:
	@for d in $(DRIVERS) $(SDK_DRIVERS) fleet; do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean:
	@rm -rf $(BIN)
