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

tidy:
	GOWORK=off go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINS)
