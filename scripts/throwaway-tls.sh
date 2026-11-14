#!/usr/bin/env bash
# Makes the throwaway TLS files every proxy's validation script mounts, in the folder it is given: tls/ holds a
# self-signed certificate for localhost, its key and a copy as the clients CA, at the paths the examples' tls keys
# name; jwt/ holds a copy of the certificate as the CA bundle auth.jwt.ca names, and a key set holding the key's public
# half for auth.jwt.keys. The caller removes the folder; no key is ever committed.
set -euo pipefail
if [[ $# -ne 1 || ! -d $1 ]]; then
  echo "usage: throwaway-tls.sh <empty folder>" >&2
  exit 2
fi
tls="$1/tls"
jwt="$1/jwt"
mkdir "$tls" "$jwt"
# openssl writes progress to stderr even when it succeeds, so its output is kept and shown only when it fails.
if ! made=$(openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -keyout "$tls/key.pem" -out "$tls/certificate.pem" 2>&1); then
  printf '%s\n' "$made" >&2
  echo "throwaway-tls.sh: openssl could not make the throwaway certificate and key" >&2
  exit 1
fi
cp "$tls/certificate.pem" "$tls/clients-ca.pem"
cp "$tls/certificate.pem" "$jwt/ca.pem"
# The key set's n is the key's modulus as unpadded base64url (RFC 7518, section 6.3.1.1): openssl prints the modulus
# in hex, printf turns each pair into its byte, and e is 65537, the exponent openssl uses.
modulus=$(openssl rsa -in "$tls/key.pem" -noout -modulus)
n=$(printf "$(sed 's/../\\x&/g' <<<"${modulus#Modulus=}")" | openssl base64 -A | tr '+/' '-_' | tr -d '=')
printf '{"keys": [{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "throwaway", "n": "%s", "e": "AQAB"}]}\n' "$n" \
  >"$jwt/jwks.json"
# A proxy runs as an unprivileged user in its image, which must read the mounted folders and their files.
chmod 755 "$tls" "$jwt"
chmod 644 "$tls"/*.pem "$jwt"/*
