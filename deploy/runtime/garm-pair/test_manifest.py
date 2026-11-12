import json
import tempfile
from unittest.mock import patch
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('pair_manifest', Path(__file__).with_name('manifest.py'))
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)


def valid():
    inputs = {'controller_commit': 'a'*40, 'controller_tree': 'b'*40, 'garm_commit': m.GARM, 'garm_patch_sha256': m.PATCH, 'bases': {name: name.split(':')[0]+'@sha256:'+'c'*64 for name in m.BASES}, 'build_frontend':'docker/dockerfile@sha256:'+'c'*64, 'dependency_files': {name: 'd'*64 for name in ('controller_go_mod','controller_go_sum','web_lock','garm_go_mod','garm_go_sum','garm_vendor_modules')}, 'dockerfiles': {'controller':'e'*64,'garm':'f'*64}, 'binaries': {name:'1'*64 for name in ('garm','capacity-provider','capacity-gateway','capacity-ledger','pair-probe')}, 'platform':'linux/amd64','toolchain':'go1.26.10','runtime_libc':'glibc 2.36'}
    images = {name:'sha256:'+'2'*64 for name in ('paired_garm','gateway','ledger_tool')}
    return {'schema':'experimental-garm-pair/v1','bundle_version':m.version(inputs,images),'inputs':inputs,'images':images,'qualification':{'public_build_evidence':True,'signed_release':False,'installed_stack_qualified':False,'deployment_authorized':False}}


class ManifestTest(unittest.TestCase):
    def test_valid_and_order_independent(self):
        m.validate(valid())
        x=valid();self.assertEqual(m.version(dict(reversed(list(x['inputs'].items()))),x['images']),x['bundle_version'])
    def test_source_patch_and_platform_are_fixed_even_with_new_id(self):
        for field, value in [('garm_commit','0'*40),('garm_patch_sha256','0'*64),('platform','linux/arm64'),('toolchain','go1.26.9'),('runtime_libc','musl')]:
            x=valid();x['inputs'][field]=value;x['bundle_version']=m.version(x['inputs'],x['images'])
            with self.assertRaises(AssertionError):m.validate(x)
    def test_content_identity_covers_all_images_and_binaries(self):
        for section,key in [('images','paired_garm'),('images','gateway'),('images','ledger_tool'),('binaries','capacity-provider'),('binaries','garm')]:
            x=valid()
            if section=='images':x[section][key]='sha256:'+'3'*64
            else:x['inputs'][section][key]='3'*64
            with self.assertRaises(AssertionError):m.validate(x)
    def test_private_extra_fields_and_deployment_claims_rejected(self):
        x=valid();x['private_config']='DO_NOT_PUBLISH'
        with self.assertRaises(AssertionError):m.validate(x)
        x=valid();x['qualification']['deployment_authorized']=True
        with self.assertRaises(AssertionError):m.validate(x)
    def test_unknown_repo_or_mutable_base_rejected(self):
        for value in ('golang:1.26.10-bookworm','other.example/golang@sha256:'+'4'*64):
            x=valid();x['inputs']['bases']['golang:1.26.10-bookworm']=value;x['bundle_version']=m.version(x['inputs'],x['images'])
            with self.assertRaises(AssertionError):m.validate(x)


class LockTest(unittest.TestCase):
    def lock(self, text):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);source=root/'input';source.write_text('# syntax=docker/dockerfile:1\n'+text)
            def refs(args):return json.dumps([args[-1].split(':')[0]+'@sha256:'+'a'*64]).encode()
            with patch.object(m.subprocess,'run'),patch.object(m.subprocess,'check_output',side_effect=refs):
                m.lock([source],[root/'output'],root/'locks')
            return (root/'output').read_text()
    def test_all_reviewed_bases_locked(self):
        text='FROM node:22.23.4-bookworm-slim AS web\nFROM golang:1.26.10-bookworm AS go\nFROM debian:bookworm-slim AS runtime\nFROM runtime AS final\n'
        result=self.lock(text)
        self.assertEqual(result.count('@sha256:'),4)
        self.assertIn('FROM runtime AS final',result)
    def test_unknown_case_whitespace_missing_and_duplicate_stages_rejected(self):
        for text in ('# syntax=other/frontend:latest\nFROM node:22.23.4-bookworm-slim AS web\n', 'FROM unknown:tag AS build\n',' from golang:1.26.10-bookworm AS build\n','FROM golang:1.26.10-bookworm AS build\n','FROM node:22.23.4-bookworm-slim AS same\nFROM golang:1.26.10-bookworm AS same\n'):
            with self.assertRaises(AssertionError):self.lock(text)


if __name__=='__main__':unittest.main()
