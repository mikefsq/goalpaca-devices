"""Installer integration tests, entirely inside temporary directories."""
import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]

class RegistrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)

    def binary(self, path, name='asiam5'):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('#!/bin/sh\ncase "$*" in\n'
                        + "'-schema json') echo '{\"driver\":\"" + name + "\",\"type\":\"telescope\"}' ;;\n"
                        + "'-schema commented') echo '{\"driver\":\"" + name + "\",\"enable\":false}' ;;\n"
                        + '*) echo "unexpected launch" >&2; exit 99 ;;\nesac\n')
        path.chmod(0o755)

    def run_cmd(self, args, **kwargs):
        subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)

    def test_helper_handles_spaces_and_runtime_path(self):
        binary = self.root / 'path with spaces' / 'alias'
        self.binary(binary, 'canonical-driver')
        records = self.root / 'drivers.conf'
        self.run_cmd(['sh', str(ROOT/'build/register-driver'), str(binary), str(records), '/usr/local/bin/alias'])
        self.assertEqual(records.read_text(), '/usr/local/bin/alias\n')
        # Registering twice must not duplicate the line or launch the executable.
        binary.unlink()
        self.run_cmd(['sh', str(ROOT/'build/register-driver'), str(binary), str(records), '/usr/local/bin/alias'])
        self.assertEqual(records.read_text(), '/usr/local/bin/alias\n')
        self.run_cmd(['sh', str(ROOT/'build/register-driver'), '/another/driver', str(records)])
        self.run_cmd(['sh', str(ROOT/'build/register-driver'), str(binary), str(records), '/usr/local/bin/alias', 'remove'])
        self.assertEqual(records.read_text(), '/another/driver\n')

    def source_tree(self):
        tree = self.root/'source'
        (tree/'build').mkdir(parents=True)
        shutil.copy(ROOT/'build/register-driver', tree/'build/register-driver')
        return tree

    def test_individual_make_install_upgrade_uninstall(self):
        tree = self.source_tree()
        module = tree/'asiam5'
        module.mkdir()
        shutil.copy(ROOT/'asiam5/Makefile', module/'Makefile')
        self.binary(module/'asiam5')
        prefix = self.root/'prefix with spaces'
        config = self.root/'config'/'devices.d'
        args = ['make', '-s', '-o', 'build', f'PREFIX={prefix}', f'CONFDIR={config}']
        self.run_cmd(args+['install'], cwd=module)
        record = config.parent/'drivers.conf'
        self.assertEqual(record.read_text().strip(), str(prefix/'bin/asiam5'))
        entry = config/'asiam5.json'
        self.assertFalse(json.loads(entry.read_text())['enable'])
        entry.write_text('{"driver":"asiam5","enable":true,"name":"keep"}')
        self.run_cmd(args+['install'], cwd=module)
        self.assertEqual(json.loads(entry.read_text())['name'], 'keep')
        self.run_cmd(args+['uninstall'], cwd=module)
        self.assertEqual(record.read_text(), '')
        self.assertTrue(entry.exists())

    def test_root_make_staging_registry(self):
        tree = self.source_tree()
        shutil.copy(ROOT/'Makefile', tree/'Makefile')
        self.binary(tree/'bin/asiam5')
        staging = self.root/'stage'
        self.run_cmd(['make', '-s', 'install', f'DESTDIR={staging}'], cwd=tree)
        record = (staging/'etc/alpacahurd/drivers.conf').read_text().strip()
        self.assertEqual(record, '/usr/local/bin/asiam5')
        self.assertFalse(json.loads((staging/'etc/alpacahurd/devices.d/asiam5.json').read_text())['enable'])
        self.run_cmd(['make', '-s', 'uninstall', f'DESTDIR={staging}'], cwd=tree)
        self.assertEqual((staging/'etc/alpacahurd/drivers.conf').read_text(), '')
        self.assertTrue((staging/'etc/alpacahurd/devices.d/asiam5.json').exists())

    def test_multi_binary_install(self):
        tree = self.source_tree()
        module = tree/'smpro'
        module.mkdir()
        shutil.copy(ROOT/'smpro/Makefile', module/'Makefile')
        for name in ['smpro-switch', 'smpro-focuser']:
            self.binary(module/name, name)
        prefix = self.root/'prefix'
        config = self.root/'etc/alpacahurd/devices.d'
        args = ['make', '-s', '-o', 'build', f'PREFIX={prefix}', f'CONFDIR={config}']
        self.run_cmd(args+['install'], cwd=module)
        for name in ['smpro-switch', 'smpro-focuser']:
            self.assertIn(str(prefix/'bin'/name), (config.parent/'drivers.conf').read_text().splitlines())
        self.run_cmd(args+['uninstall'], cwd=module)
        self.assertEqual((config.parent/'drivers.conf').read_text(), '')

    def test_debian_scripts_register_and_remove(self):
        script = (ROOT/'build/build-deb').read_text()
        function = script[script.index('write_maintainer_scripts () {'):script.index('[[ -n $version ]]')]
        control = self.root/'control'
        control.mkdir()
        self.run_cmd(['bash','-s','--',str(control)], input=(function+'\nwrite_maintainer_scripts "$1" asiam5\n').encode())
        binary = self.root/'bin/asiam5'
        self.binary(binary)
        config = self.root/'etc/alpacahurd'
        for name in ['postinst', 'postrm']:
            p = control/name
            p.write_text(p.read_text().replace('/usr/bin/asiam5',str(binary))
                         .replace('/etc/alpacahurd',str(config))
                         .replace('/run/systemd/system',str(self.root/'no-systemd')))
        self.run_cmd(['sh',str(control/'postinst'),'configure'])
        record = config/'drivers.conf'
        self.assertEqual(record.read_text().strip(),str(binary))
        self.run_cmd(['sh',str(control/'postrm'),'remove'])
        self.assertEqual(record.read_text(), '')
        self.assertTrue((config/'devices.d/asiam5.json').exists())

if __name__ == '__main__':
    unittest.main()
