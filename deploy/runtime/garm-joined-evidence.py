"""Closed public result for the hosted joined fixture, not deployment authority."""
import json
from pathlib import Path
import re
import sys

STOCK_ID = 'sha256:cb7c076ddbf24a67a9aef4a4e61ace495e9ffc7bcb69df3bf5e89275a26b79f6'


def record(source_commit, run_id, run_attempt, stock, gateway, ingress, ledger, gateway_sha, ingress_sha):
    # source_commit is actual git HEAD, possibly GitHub's synthetic PR merge.
    # Never label GITHUB_SHA or an unproven event payload as the tested PR head.
    if not re.fullmatch('[0-9a-f]{40}', source_commit):
        raise ValueError('invalid checkout identity')
    if not all(re.fullmatch('[1-9][0-9]{0,19}', v) for v in (run_id, run_attempt)):
        raise ValueError('invalid run identity')
    images = (stock, gateway, ingress, ledger)
    if (not all(re.fullmatch('sha256:[0-9a-f]{64}', v) for v in images)
            or len(set(images)) != 4 or stock != STOCK_ID):
        raise ValueError('invalid image identity')
    if not all(re.fullmatch('[0-9a-f]{64}', v) for v in (gateway_sha, ingress_sha)):
        raise ValueError('invalid executable identity')
    return {
        'schema': 'disposable-three-service-preboot/v2',
        'source_commit': source_commit, 'run_id': run_id, 'run_attempt': run_attempt,
        'stock_image': stock, 'gateway_image': gateway,
        'ingress_image': ingress, 'ledger_image': ledger,
        'gateway_executable_sha256': gateway_sha,
        'ingress_executable_sha256': ingress_sha,
        'actual_three_binaries_started': True,
        'exact_four_ipv4_loopback_listeners_verified': True,
        'shared_isolated_network': True, 'host_ports_published': False,
        'stock_uninitialized': True, 'supervisor_present': False,
        'admission_ready': False, 'upstream_inventory_integrated': False,
        'managed_topology_qualified': False, 'startup_authority': False,
        'deployment_authorized': False,
    }


def main(args):
    if len(args) != 10:
        raise ValueError('invalid arguments')
    value = record(*args[1:])
    # Exclusive creation: the launcher clears its fixed stale result before work.
    with Path(args[0]).open('x') as output:
        output.write(json.dumps(value, sort_keys=True, indent=2) + '\n')


if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except (ValueError, OSError):
        print('Joined public evidence unavailable', file=sys.stderr)
        raise SystemExit(1)
