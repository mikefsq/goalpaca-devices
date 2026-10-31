NAME := asiair
ALIASES := asiair-switch

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

.PHONY: build install seed uninstall tidy test vet clean pi-only
PI_NAME := asiair

# The driver drives Raspberry Pi hardware. Other hosts cross-compile with make pi
# in the repository root.
pi-only:
ifneq ($(UNAME_S),Linux)
	@echo "$(PI_NAME): Raspberry Pi hardware only. Build on the Pi, or run make pi in the repository root" >&2; exit 1
endif

build: pi-only
	go build -o $(NAME) ./cmd/$(NAME)

install: pi-only
	@sh ../build/check-fresh $(NAME) ..
	install -d "$(PREFIX)/bin"
	install -m 0755 $(NAME) "$(PREFIX)/bin/$(NAME)"
	@sh ../build/register-driver "$(PREFIX)/bin/$(NAME)" "$(REGISTRYFILE)"
	@for a in $(ALIASES); do ln -sf $(NAME) "$(PREFIX)/bin/$$a"; echo "link   $(PREFIX)/bin/$$a -> $(NAME)"; done
	@echo "start or restart alpacahurd to use it: $(RESTART_HURD)"

seed: pi-only ## write a disabled devices.d template; keeps an existing file
	@sh ../build/check-fresh $(NAME) ..
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
	@sh ../build/register-driver "$(PREFIX)/bin/$(NAME)" "$(REGISTRYFILE)" "$(PREFIX)/bin/$(NAME)" remove
	@for a in $(ALIASES); do rm -f "$(PREFIX)/bin/$$a"; done
	rm -f "$(PREFIX)/bin/$(NAME)"

tidy:
	GOWORK=off go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(NAME)
