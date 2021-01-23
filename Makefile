VERSION ?= 0.2.0
REGISTRY ?= quay.io
ORG ?= rh-nfv-int

CONTAINER_CLI ?= docker
CLUSTER_CLI ?= oc

IMG ?= $(REGISTRY)/$(ORG)/cnf-app-mac-fetch:v$(VERSION)

all: build

build:
	$(CONTAINER_CLI) build . -t ${IMG}
	$(CONTAINER_CLI) push ${IMG}
