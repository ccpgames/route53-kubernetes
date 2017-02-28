FROM alpine:3.4

# uncomment this line if your service needs to make https calls
 RUN apk add -U ca-certificates

COPY route53-kubernetes /route53-kubernetes
ENTRYPOINT ["/route53-kubernetes"]
