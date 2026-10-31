BINS := smpro-switch smpro-focuser

PREFIX ?= /usr/local
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CONFDIR ?= /Library/Application Support/alpacahurd/devices.d
else
CONFDIR ?= /etc/alpacahurd/devices.d
endif

ifeq ($(shell uname -s),Darwin)
RESTART_HURD := sudo launchctl kickstart -k system/com.mikefsq.alpacahurd
else
RESTART_HURD := sudo systemctl restart alpacahurd
endif

REGISTRYFILE ?= $(CONFDIR)/../drivers.conf

.PHONY: build install seed uninstall tidy test vet clean pi-only $(BINS)
PI_NAME := smpro-switch and smpro-focuser

# The driver drives Raspberry Pi hardware. Other hosts cross-compile with make pi
# in the repository root.
pi-only:
ifneq ($(UNAME_S),Linux)
	@echo "$(PI_NAME): Raspberry Pi hardware only. Build on the Pi, or run make pi in the repository root" >&2; exit 1
endif

build: $(BINS)

$(BINS): pi-only
	go build -o $@ ./cmd/$@

install: pi-only
	@for b in $(BINS); do sh ../build/check-fresh "$$b" .. || exit 1; done
	install -d "$(PREFIX)/bin"
	@for b in $(BINS); do \
		install -m 0755 "$$b" "$(PREFIX)/bin/$$b"; \
		echo "install $(PREFIX)/bin/$$b"; \
		sh ../build/register-driver "$(PREFIX)/bin/$$b" "$(REGISTRYFILE)" || exit 1; \
	done
	@echo "start or restart alpacahurd to use it: $(RESTART_HURD)"

seed: pi-only ## write disabled devices.d templates; keeps existing files
	@for b in $(BINS); do sh ../build/check-fresh "$$b" .. || exit 1; done
	@install -d "$(CONFDIR)"
	@for b in $(BINS); do \
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
	@for b in $(BINS); do sh ../build/register-driver "$(PREFIX)/bin/$$b" "$(REGISTRYFILE)" "$(PREFIX)/bin/$$b" remove || exit 1; rm -f "$(PREFIX)/bin/$$b"; echo "remove  $(PREFIX)/bin/$$b"; done

tidy:
	GOWORK=off go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINS)
