# goalpaca_devices — build the standalone Alpaca driver modules (each its own
# Go module) into ./bin. CGO is enabled for the ASI drivers and is harmless for
# the pure-Go telescope drivers.
#
#   make            build every driver into ./bin
#   make <driver>   build one (e.g. make tenmicron)
#   make vet        go vet every module
#   make tidy       go mod tidy every module
#   make clean      remove ./bin

DRIVERS := tenmicron asiam5 rst onstep asiccd asieaf asicaa asiefw
BIN     := bin

.PHONY: build vet tidy clean $(DRIVERS)

build: $(DRIVERS)

$(DRIVERS): | $(BIN)
	@echo "building $@"
	@cd $@ && CGO_ENABLED=1 go build -o ../$(BIN)/$@ .

$(BIN):
	@mkdir -p $(BIN)

vet:
	@for d in $(DRIVERS); do echo "vet $$d"; (cd $$d && go vet ./...) || exit 1; done

tidy:
	@for d in $(DRIVERS); do echo "tidy $$d"; (cd $$d && go mod tidy) || exit 1; done

clean:
	@rm -rf $(BIN)
