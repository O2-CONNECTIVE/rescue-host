"""接続版の準備manifestだけを検査する。配布・起動・認可の成功は返さない。"""
import json
from pathlib import Path
import re
import sys

from verify_tauri_evaluation import SafeParser, VerificationError, require

SCHEMA = 'rescue-host-connected-evaluation-plan/1'
COMPONENTS = {'gui': 'bin/rescue-host-shell.exe', 'service': 'bin/rescue-service-host.exe',
              'backend': 'bin/rescue-backend.exe'}
REQUIRED_FILES = set(COMPONENTS.values()) | {
    'LICENSE', 'locks/Cargo.lock', 'locks/package-lock.json', 'locks/go.mod', 'locks/go.sum',
    'licenses/gui/DEPENDENCIES.json', 'licenses/service/DEPENDENCIES.json',
    'licenses/backend/DEPENDENCIES.json', 'licenses/FRONTEND_THIRD_PARTY_NOTICES.txt'}
BLOCKERS = ['SERVICE_PROVISIONING_UNSET', 'PROTECTED_STDIO_UNVERIFIED',
            'OPERATOR_APPROVAL_UNWIRED', 'RUNTIME_VERSION_UNVERIFIED',
            'DEPENDENCIES_UNVERIFIED', 'WINDOWS_PROTECTION_UNVERIFIED',
            'DISTRIBUTION_TRUST_UNDECIDED']


def is_hex_is_hex_digest(value, length):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{' + str(length) + '}', value) is not None


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'PLAN_DUPLICATE_FIELD')
        result[key] = value
    return result


def validate(manifest, source, hashes):
    """宣言の内部整合だけ。filesに記載した実bytesは読み取らない。"""
    require(is_hex_digest(source, 40) and set(hashes) == set(COMPONENTS)
            and all(is_hex_digest(value, 64) for value in hashes.values()), 'PLAN_EXPECTATION_INVALID')
    fields = {'schema', 'repository', 'sourceCommit', 'platform', 'activation',
              'productionGateClosed', 'protocols', 'components', 'files'}
    require(isinstance(manifest, dict) and set(manifest) == fields, 'PLAN_SCHEMA_INVALID')
    require(manifest['schema'] == SCHEMA and manifest['repository'] == 'O2-CONNECTIVE/rescue-host'
            and manifest['platform'] == 'windows-x64', 'PLAN_SCHEMA_INVALID')
    require(manifest['sourceCommit'] == source, 'PLAN_SOURCE_MISMATCH')
    require(manifest['activation'] == 'blocked' and manifest['productionGateClosed'] is True,
            'PLAN_ACTIVATION_FORBIDDEN')
    require(manifest['protocols'] == {'control': 'rescue-host-control/1',
                                    'bootstrap': 'rescue-host-bootstrap/1'}, 'PLAN_PROTOCOL_MISMATCH')
    files = manifest['files']
    require(isinstance(files, dict) and set(files) == REQUIRED_FILES
            and all(is_hex_digest(value, 64) for value in files.values()), 'PLAN_FILE_SET_INVALID')
    components = manifest['components']
    require(isinstance(components, dict) and set(components) == set(COMPONENTS),
            'PLAN_COMPONENT_SET_INVALID')
    for role, file in COMPONENTS.items():
        component = components[role]
        require(isinstance(component, dict)
                and set(component) == {'path', 'sourceCommit', 'sha256', 'version'},
                'PLAN_COMPONENT_INVALID')
        require(component['path'] == file, 'PLAN_COMPONENT_INVALID')
        require(component['sourceCommit'] == source, 'PLAN_SOURCE_MISMATCH')
        require(component['sha256'] == hashes[role] == files[file], 'PLAN_BINARY_HASH_MISMATCH')
        require(component['version'] == 'unverified', 'PLAN_RUNTIME_CLAIM_FORBIDDEN')
    return {'check': 'connected-package-plan', 'status': 'blocked', 'manifestValid': True,
            'productionGateClosed': True, 'code': 'CONNECTED_PACKAGE_NOT_RELEASEABLE',
            'blockers': BLOCKERS.copy()}


def main():
    parser = SafeParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, required=True)
    parser.add_argument('--source', required=True)
    for role in COMPONENTS:
        parser.add_argument('--' + role + '-sha256', required=True)
    try:
        args = parser.parse_args()
        require(args.manifest.is_file() and not args.manifest.is_symlink(), 'PLAN_INPUT_INVALID')
        with args.manifest.open('rb') as stream:
            data = stream.read(128 * 1024 + 1)
        require(len(data) <= 128 * 1024, 'PLAN_SIZE_LIMIT')
        manifest = json.loads(data, object_pairs_hook=strict_object)
        hashes = {role: getattr(args, role + '_sha256') for role in COMPONENTS}
        report = validate(manifest, args.source, hashes)
        code = 2  # 整合しても起動前checkや配布可否には進まない。
    except VerificationError as error:
        report = {'check': 'connected-package-plan', 'status': 'fail', 'code': str(error)}
        code = 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, RecursionError):
        report = {'check': 'connected-package-plan', 'status': 'fail', 'code': 'PLAN_INVALID'}
        code = 1
    print(json.dumps(report, ensure_ascii=True, sort_keys=True))
    return code


if __name__ == '__main__':
    sys.exit(main())
