#!/usr/bin/env bash
set -euo pipefail

: "${TWC_TOKEN:?set TWC_TOKEN without printing it}"

api="${TWC_API_URL:-https://api.timeweb.cloud/api/v1}"
mode="${PAAS_IPV4_MODE:-reserved}"
if [[ "${mode}" != "reserved" && "${mode}" != "live" ]]; then
  echo "PAAS_IPV4_MODE must be reserved or live" >&2
  exit 1
fi
payload="$(curl --fail --silent --show-error \
  -H "Authorization: Bearer ${TWC_TOKEN}" \
  "${api}/floating-ips")"

approved='[
  {"id":"6c842a77-1f4a-436d-ac3e-f86fd1af9454","ip":"5.42.126.95","live_type":"balancer"},
  {"id":"7af71678-a5b2-47bc-9b78-ceb99cd20780","ip":"72.56.234.22","live_type":"router"}
]'
protected='[
  {"id":"1e7352b8-f974-47e7-bd83-ea3aeb205f0c","ip":"5.42.106.8"},
  {"id":"28f5ce64-53d5-46ab-a93a-fbb098576101","ip":"72.56.246.80"},
  {"id":"9f4fd42b-c1fe-4f63-b6b9-ff4380ae4a62","ip":"85.193.87.39"}
]'

jq -e --arg mode "${mode}" --argjson approved "${approved}" --argjson protected "${protected}" '
  (.floating_ips // .ips // .data // []) as $actual
  | all($protected[]; . as $want
      | any($actual[]; .id == $want.id and .ip == $want.ip))
  and all($approved[]; . as $want
      | any($actual[];
          .id == $want.id
          and .ip == $want.ip
          and .availability_zone == "msk-1"
          and (if $mode == "reserved"
               then (.resource_id == null and .resource_type == null)
               else (.resource_id != null and .resource_type == $want.live_type)
               end)))
' <<<"${payload}" >/dev/null

echo "PRESERVED_IPV4_GATE=PASS mode=${mode}"
