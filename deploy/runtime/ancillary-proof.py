"""Private bounded adapter to the pinned Go derivation checker. No extraction."""
import base64
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile

def require(ok):
    if not ok:
        raise ValueError('ancillary proof unavailable')

def gather(path, ids, metadata):
    require(len(ids)==1)
    expected=next(iter(ids.values()))
    require(type(expected) is str and re.fullmatch('sha256:[0-9a-f]{64}',expected))
    config_path='blobs/sha256/'+expected[7:]
    with tarfile.open(path,'r:') as archive:
        entries={}
        for member in archive:
            require(len(entries)<1024 and member.name not in entries)
            require(member.isfile() or member.isdir())
            require(not member.pax_headers and not member.issparse())
            require(not member.name.startswith('/') and '..' not in member.name.split('/'))
            entries[member.name]=member
        def raw(name,limit):
            member=entries.get(name)
            require(member is not None and member.isfile() and 0<member.size<=limit)
            value=archive.extractfile(member).read(limit+1)
            require(len(value)==member.size)
            if name.startswith('blobs/'):
                require(re.fullmatch('blobs/sha256/[0-9a-f]{64}',name))
                require(hashlib.sha256(value).hexdigest()==name.rsplit('/',1)[1])
            return value
        def unique(pairs):
            obj={}
            for key,value in pairs:
                require(key not in obj);obj[key]=value
            return obj
        def doc(name):
            return json.loads(raw(name,256*1024),object_pairs_hook=unique)
        docker,index=doc('manifest.json'),doc('index.json')
        require(type(docker) is list and len(docker)==1 and type(docker[0]) is dict)
        row=docker[0]
        require(row.get('Config')==config_path and row.get('RepoTags') in (None,[]))
        require(type(row.get('Layers')) is list and 1<=len(row['Layers'])<=256)
        used={'manifest.json','index.json','oci-layout',config_path}
        for layer in row['Layers']:
            require(type(layer) is str and re.fullmatch('blobs/sha256/[0-9a-f]{64}',layer));used.add(layer)
        require(type(index) is dict and type(index.get('manifests')) is list and len(index['manifests'])==1)
        desc=index['manifests'][0]
        require(type(desc) is dict and desc.get('mediaType')=='application/vnd.oci.image.manifest.v1+json')
        digest=desc.get('digest')
        require(type(digest) is str and re.fullmatch('sha256:[0-9a-f]{64}',digest))
        name='blobs/sha256/'+digest[7:]
        manifest=doc(name);used.add(name)
        require(type(desc.get('size')) is int and entries[name].size==desc['size'])
        require(type(manifest) is dict and type(manifest.get('config')) is dict
                and manifest['config'].get('digest')==expected)
        require(type(manifest.get('layers')) is list and len(manifest['layers'])==len(row['Layers']))
        require([v.get('digest') if type(v) is dict else None for v in manifest['layers']]
                ==['sha256:'+v.rsplit('/',1)[1] for v in row['Layers']])
        extras={n for n,m in entries.items() if m.isfile()}-used
        require(1<=len(extras)<=256)
        nodes={}
        for name in extras:
            require(re.fullmatch('blobs/sha256/[0-9a-f]{64}',name))
            nodes['sha256:'+name.rsplit('/',1)[1]]=base64.b64encode(raw(name,256*1024)).decode('ascii')
        return {'image_id':expected,'config':base64.b64encode(raw(config_path,16*1024**2)).decode('ascii'),
                'nodes':nodes,'exporter':metadata}

def prove(path, ids, metadata, binary):
    payload=json.dumps(gather(path,ids,metadata),separators=(',',':')).encode()
    require(len(payload)<=112*1024**2)
    # Only the reviewed locally built helper is supplied by the hosted harness.
    # It emits a fixed bounded result; no private bytes/errors are relayed.
    completed=subprocess.run([str(binary)],input=payload,stdout=subprocess.PIPE,
                             stderr=subprocess.DEVNULL,timeout=30)
    require(completed.returncode==0 and 0<len(completed.stdout)<=4096)
    value=json.loads(completed.stdout)
    require(type(value) is dict and set(value)=={'schema','derived_nodes','matched_nodes',
            'ancillary_derivation_verified','archive_accepted','deployment_authorized'})
    require(value['schema']=='moby-v28-ancillary-proof/v1'
            and type(value['derived_nodes']) is int and 1<=value['derived_nodes']<=256
            and type(value['matched_nodes']) is int and value['matched_nodes']==value['derived_nodes']
            and value['ancillary_derivation_verified'] is True
            and value['archive_accepted'] is False and value['deployment_authorized'] is False)
    return value
