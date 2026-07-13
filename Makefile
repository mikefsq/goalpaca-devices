# goalpaca_devices — build the standalone Alpaca driver modules (each its own Go
# module) into ./bin.
#
#   make              build every pure-Go driver into ./bin
#   make help         list the targets
#   make <name>       build one (e.g. make tenmicron)
#   make sdk          build the cgo ZWO-SDK drivers (needs libASICamera2)
#   make all          build + sdk
#   make sim          build the coupled guide sim (mount + camera, one shared sky)
#   make alpacasim    run goalpaca's one-of-every-type protocol sim (not guidable)
#   make vet          go vet every module
#   make tidy         go mod tidy every module
#   make clean        remove ./bin and any per-cmd build outputs

# Pure-Go drivers — build anywhere, no vendor SDK. astrocam is the RE'd ZWO camera
# driver (module asicam-alpaca) that replaced the SDK-based asicam.
DRIVERS := tenmicron asiam5 rst onstep astrocam asieaf asiefw \
           focuscube focuslynx oasisfoc oasisfw mgpbox unihedron sim

# cgo drivers that link the ZWO SDK (libASICamera2) — opt-in, not in the default build.
SDK_DRIVERS := asiccd asicaa

BIN := bin

.PHONY: build help all sdk alpacasim vet tidy clean $(DRIVERS) $(SDK_DRIVERS)

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

all: build sdk ## build + the cgo ZWO-SDK drivers

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

sdk: $(SDK_DRIVERS) ## build the cgo ZWO-SDK drivers (needs libASICamera2)

$(SDK_DRIVERS): | $(BIN)
	@echo "building $@ (cgo + ZWO SDK)"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ ./cmd/$@

# sim (in DRIVERS above) builds bin/sim: the coupled mount + guide-camera pair on one
# shared simulated sky, so PHD2 can calibrate and guide a closed loop with no hardware.
# alpacasim runs goalpaca's uncoupled one-of-every-type sim — a ConformU/protocol
# target, not guidable (its camera frames don't respond to mount pulses).
alpacasim: ## run goalpaca's one-of-every-type protocol sim (not guidable)
	@cd ../goalpaca && go run ./cmd/alpacasim

$(BIN):
	@mkdir -p $(BIN)

vet: ## go vet every module
	@for d in $(DRIVERS) $(SDK_DRIVERS); do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done

tidy: ## go mod tidy every module
	@for d in $(DRIVERS) $(SDK_DRIVERS); do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean: ## remove ./bin and any per-cmd build outputs
	@rm -rf $(BIN)
	@rm -f $(foreach d,$(DRIVERS) $(SDK_DRIVERS),$(d)/cmd/$(d)/$(d))
