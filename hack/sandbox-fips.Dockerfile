# Default sandbox image for FIPS mode. OpenSSL here switches to its FIPS
# provider when the guest kernel runs with fips=1, which the Helm chart sets.
# The build forces that mode once to prove the provider loads and MD5 is refused.
ARG UBI=9.8
FROM registry.access.redhat.com/ubi9/ubi-minimal:$UBI
RUN microdnf install -y tar gzip openssl && microdnf clean all \
    && OPENSSL_FORCE_FIPS_MODE=1 openssl list -providers | grep -q '^  fips' \
    && ! OPENSSL_FORCE_FIPS_MODE=1 openssl md5 /etc/os-release >/dev/null 2>&1
