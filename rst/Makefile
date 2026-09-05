NAME := rst
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

tidy:
	GOWORK=off go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(NAME)
