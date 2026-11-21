#!/usr/bin/env bash
set -euo pipefail

version="0.160.0"
asset="opampsupervisor_${version}_linux_amd64"
base="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fopampsupervisor%2Fv${version}"
destination="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/${asset}"

curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error "${base}/${asset}" --output "${destination}"
curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error "${base}/${asset}.sha256" --output "${destination}.sha256"
expected="$(awk 'NR == 1 { print $1 }' "${destination}.sha256" | tr -d '*')"
if [[ ! "${expected}" =~ ^[[:xdigit:]]{64}$ ]]; then
	printf 'invalid upstream Supervisor checksum file\n' >&2
	exit 1
fi
actual="$(sha256sum "${destination}" | awk '{ print $1 }')"
if [[ "${actual}" != "${expected}" ]]; then
	printf 'Supervisor v%s checksum mismatch\n' "${version}" >&2
	exit 1
fi
chmod 0755 "${destination}"
build_info="$(go version -m "${destination}")"
if ! grep -Fq $'mod\tgithub.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor\tv0.160.0' <<< "${build_info}"; then
	printf 'downloaded Supervisor does not contain pinned module v%s\n%s\n' "${version}" "${build_info}" >&2
	exit 1
fi
printf 'OPAMP_SUPERVISOR_BIN=%s\n' "${destination}" >> "${GITHUB_ENV}"
printf 'Verified official OpAMP Supervisor v%s SHA-256 %s\n' "${version}" "${actual}"
