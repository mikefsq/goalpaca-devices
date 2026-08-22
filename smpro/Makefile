# smpro — the StellarMate SM Pro controller board, its own Go module. The repo's
# top-level Makefile is a convenience over these per-module files; this one is
# all a single module needs.
#
#   make            build ./smpro-switch and ./smpro-focuser
#   make install    install to $(PREFIX)/bin and seed a devices.d config each (root)
#   make uninstall  remove the binaries (config kept)
#   make tidy       resolve go.mod from published modules (no sibling checkouts)
#   make test / vet / clean
#
# The board presents two ASCOM device types over hardware that shares nothing:
# the Switch drives the I2C expander, DAC, SPI ADC and dew PWM, the Focuser
# drives the TMC2209 on its own UART. Each opens only its own subsystems, so
# they are two binaries and two processes, and either can run without the other.
# There is no combined binary: one that opened the whole board would contend
# with both of these.

BINS := smpro-switch smpro-focuser

PREFIX ?= /usr/local
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CONFDIR ?= /Library/Application Support/alpacahurd/devices.d
else
CONFDIR ?= /etc/alpacahurd/devices.d
endif

.PHONY: build install uninstall tidy test vet clean $(BINS)

build: $(BINS)

$(BINS):
	go build -o $@ ./cmd/$@

# install copies each binary into PREFIX/bin, where alpacahurd resolves a driver
# by looking for a file named after it, and seeds a disabled devices.d config
# from the binary's own schema when none exists; an existing config is kept.
install: build
	install -d "$(PREFIX)/bin"
	@install -d "$(CONFDIR)"
	@for b in $(BINS); do \
		install -m 0755 "$$b" "$(PREFIX)/bin/$$b"; \
		echo "install $(PREFIX)/bin/$$b"; \
		if [ -f "$(CONFDIR)/$$b.json" ]; then \
			echo "keep    $(CONFDIR)/$$b.json"; \
		elif "./$$b" -schema commented > "$(CONFDIR)/$$b.json" 2>/dev/null; then \
			echo "wrote   $(CONFDIR)/$$b.json (disabled; edit it, then enable)"; \
		else \
			rm -f "$(CONFDIR)/$$b.json"; \
			echo "no seed: $$b prints no -schema commented; write $(CONFDIR)/$$b.json by hand"; \
		fi; \
	done

uninstall:
	@for b in $(BINS); do rm -f "$(PREFIX)/bin/$$b"; echo "remove  $(PREFIX)/bin/$$b"; done

# tidy pins every dependency from the module proxy, the way a third-party
# clone resolves them, so it proves the module builds without its sibling
# checkouts (the repo's go.work covers those during development).
tidy:
	GOWORK=off go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINS)
