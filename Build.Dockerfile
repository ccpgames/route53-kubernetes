FROM golang:1.7-alpine

RUN apk add -U git

RUN mkdir -p /go/src/github.com/ccpgames/route53-kubernetes
WORKDIR /go/src/github.com/ccpgames/route53-kubernetes

# copy code first so that we are sure to always get
#  the newest version of the dependencies
COPY . /go/src/github.com/ccpgames/route53-kubernetes

CMD ["/go/bin/route53-kubernetes"]

# we can't "go-get"" client-go, because it has an unstable master branch
RUN git clone --depth 1 --branch release-1.5 -- https://github.com/kubernetes/client-go /go/src/k8s.io/client-go

# get all public dependencies, including dependencies of submodules and all test dependencies
# note that private repo dependencies MUST be vendored for this to succeed
RUN go get -t ./...
# run all tests, including those for submodules
RUN go test ./...
# install the current application to /go/bin
RUN go install
