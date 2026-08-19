# astrocam — a standalone ASCOM Alpaca driver, its own Go module. The repo's
# top-level Makefile is a convenience over these per-module files; this one is
# all a single module needs, and the template for a new third-party module.
#
#   make            build ./astrocam
#   make install    install to $(PREFIX)/bin and seed a devices.d config (root)
#   make uninstall  remove the binary and its symlinks (config kept)
#   make tidy       resolve go.mod from published modules (no sibling checkouts)
#   make test / vet / clean

NAME := astrocam
# Driver names this module registers when they differ from the binary's (a hub
# module registering several devices). install writes one symlink per name, so
# a host (alpacahurd) resolves each driver name to this binary.
ALIASES :=

PREFIX ?= /usr/local
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CONFDIR ?= /Library/Application Support/alpacahurd/devices.d
else
CONFDIR ?= /etc/alpacahurd/devices.d
endif

.PHONY: build install uninstall tidy test vet clean

build:
	go build -o $(NAME) ./cmd/$(NAME)

# install copies the binary (with its driver-name symlinks) into PREFIX/bin
# and seeds a disabled devices.d config from the binary's own schema when none
# exists; an existing config is never touched.
install: build
	install -d "$(PREFIX)/bin"
	install -m 0755 $(NAME) "$(PREFIX)/bin/$(NAME)"
	@for a in $(ALIASES); do ln -sf $(NAME) "$(PREFIX)/bin/$$a"; echo "link   $(PREFIX)/bin/$$a -> $(NAME)"; done
	@install -d "$(CONFDIR)"
	@if [ -f "$(CONFDIR)/$(NAME).json" ]; then \
		echo "keep   $(CONFDIR)/$(NAME).json"; \
	elif "./$(NAME)" -schema commented > "$(CONFDIR)/$(NAME).json" 2>/dev/null; then \
		echo "wrote  $(CONFDIR)/$(NAME).json (disabled; edit it, then enable)"; \
	else \
		rm -f "$(CONFDIR)/$(NAME).json"; \
		echo "no seed: $(NAME) prints no -schema commented; write $(CONFDIR)/$(NAME).json by hand"; \
	fi

uninstall:
	@for a in $(ALIASES); do rm -f "$(PREFIX)/bin/$$a"; done
	rm -f "$(PREFIX)/bin/$(NAME)"

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
	rm -f $(NAME)
