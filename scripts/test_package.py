import copy
import importlib.util
import json
import tempfile
import unittest
import zipfile
from pathlib import Path

spec = importlib.util.spec_from_file_location('validator', Path(__file__).with_name('validate-package.py'))
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)

BASE = {'schemaVersion': 1, 'protocolVersion': 1, 'id': 'test-plugin', 'name': 'Test',
        'version': '1.0.0', 'level': 'standard', 'runAs': 'ludus', 'executable': 'plugin',
        'ui': 'ui/index.html', 'rangeScoped': True, 'vmHooks': {},
        'routes': [{'name': 'Status', 'method': 'GET', 'pattern': '/status', 'scope': 'plugin'}]}


class PackageTests(unittest.TestCase):
    def package(self, manifest, extras=None):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        path = Path(folder.name) / 'package.zip'
        with zipfile.ZipFile(path, 'w') as archive:
            archive.writestr('plugin.json', json.dumps(manifest))
            if manifest.get('executable'):
                archive.writestr('plugin', b'executable fixture')
            if manifest.get('ui'):
                archive.writestr('ui/index.html', '<p>Test</p>')
            for name, content in (extras or {}).items():
                archive.writestr(name, content)
        return path

    def test_standard_sdk_package(self):
        self.assertEqual(validator.validate(self.package(BASE)), BASE)

    def test_ui_only(self):
        manifest = {k: v for k, v in BASE.items() if k not in ('executable', 'protocolVersion', 'routes')}
        validator.validate(self.package(manifest))

    def test_system_hooks_and_api_routes(self):
        manifest = copy.deepcopy(BASE)
        manifest.update(level='system', runAs='root', vmHooks={'start': True}, forwardCallerCredentials=True)
        manifest['routes'][0]['scope'] = 'api'
        validator.validate(self.package(manifest))

    def test_app_with_vm_actions(self):
        actions = [{'name': 'LudusVMActions', 'method': 'GET', 'pattern': '/vm-actions', 'scope': 'plugin'},
                   {'name': 'LudusVMActionLaunch', 'method': 'POST', 'pattern': '/vm-actions/launch', 'scope': 'plugin'}]
        validator.validate(self.package({**BASE, 'app': True, 'vmActions': True, 'routes': BASE['routes'] + actions}))
        for changes in ({'vmActions': True, 'routes': BASE['routes'] + actions}, {'app': True, 'vmActions': True}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                validator.validate(self.package({**BASE, **changes}))
        ui_only = {k: v for k, v in BASE.items() if k not in ('executable', 'protocolVersion', 'routes')}
        with self.assertRaises(ValueError):
            validator.validate(self.package({**ui_only, 'app': True}))

    def test_rejects_legacy_protocol_and_invalid_permissions(self):
        cases = [{'protocolVersion': 2}, {'level': 'unknown'}, {'runAs': 'admin'},
                 {'vmHooks': {'start': True}}, {'level': 'system', 'vmHooks': {'start': True}},
                 {'routes': [{'name': 'Status', 'method': 'GET', 'pattern': '/status', 'scope': 'api'}]},
                 {'forwardCallerCredentials': 'true'}, {'runAs': False}, {'level': []}, {'unknown': True}]
        for changes in cases:
            with self.subTest(changes=changes):
                with self.assertRaises(ValueError):
                    validator.validate(self.package({**BASE, **changes}))

    def test_rejects_route_collisions_and_reserved_api_paths(self):
        for routes in [BASE['routes'] * 2,
                       [{'name': 'A', 'method': 'GET', 'pattern': '/status'}, {'name': 'B', 'method': 'GET', 'pattern': '/status'}],
                       [{'name': 'A', 'method': 'GET', 'pattern': '/plugins/status', 'scope': 'api'}]]:
            with self.subTest(routes=routes), self.assertRaises(ValueError):
                validator.validate(self.package({**BASE, 'level': 'system', 'routes': routes}))

    def test_rejects_old_openapi_and_extra_entries(self):
        for name in ('openapi.json', 'ui/', '../plugin', 'README.md'):
            with self.subTest(name=name), self.assertRaises(ValueError):
                validator.validate(self.package(BASE, {name: '{}'}))

    def test_ui_only_cannot_declare_backend_features(self):
        manifest = {k: v for k, v in BASE.items() if k not in ('executable', 'protocolVersion')}
        with self.assertRaises(ValueError):
            validator.validate(self.package(manifest))

    def test_nonregular_entry(self):
        path = self.package({k: v for k, v in BASE.items() if k != 'ui'})
        with zipfile.ZipFile(path, 'a') as archive:
            info = zipfile.ZipInfo('ui/index.html')
            info.create_system = 3
            info.external_attr = 0o120777 << 16
            archive.writestr(info, '/etc/passwd')
        with self.assertRaises(ValueError):
            validator.validate(path)


if __name__ == '__main__':
    unittest.main()
