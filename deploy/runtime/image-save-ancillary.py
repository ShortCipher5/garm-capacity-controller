"""Version-agnostic legacy-blob diagnostics, never an acceptance predicate."""
import hashlib
import json
import re
import tarfile

FIELDS = {'id','parent','comment','created','container','container_config',
          'docker_version','author','config','architecture','variant','os','Size'}
SHARED = ('created','comment','container','docker_version','author','config','architecture','variant','os')


def require(ok):
    if not ok:
        raise ValueError('ancillary structure unavailable')


def unique(pairs):
    value={}
    for k,v in pairs:
        require(k not in value)
        value[k]=v
    return value


def blob_path(digest):
    require(type(digest) is str and re.fullmatch('sha256:[0-9a-f]{64}',digest))
    return 'blobs/sha256/'+digest[7:]


def describe(path, ids):
    with tarfile.open(path,'r:') as archive:
        entries={}
        for member in archive:
            require(len(entries)<1024 and member.name not in entries)
            require(member.isfile() or member.isdir())
            require(not member.name.startswith('/') and '..' not in member.name.split('/'))
            entries[member.name]=member
        def read(name, limit=256*1024, digest=None):
            member=entries.get(name)
            require(member is not None and member.isfile() and 0<member.size<=limit)
            raw=archive.extractfile(member).read(limit+1)
            require(len(raw)==member.size)
            if digest is not None:
                require(hashlib.sha256(raw).hexdigest()==digest)
            return json.loads(raw,object_pairs_hook=unique)
        used={'manifest.json','index.json','oci-layout'}
        docker=read('manifest.json')
        index=read('index.json')
        require(type(docker) is list and 1<=len(docker)<=4)
        require(type(index) is dict and type(index.get('manifests')) is list and len(index['manifests'])<=4)
        configs=[]
        for row in docker:
            require(type(row) is dict and type(row.get('Layers')) is list and len(row['Layers'])<=256)
            name=row.get('Config')
            require(name in {blob_path(v) for v in ids.values()})
            config=read(name,16*1024**2,name.rsplit('/',1)[1])
            require(type(config) is dict)
            configs.append(config);used.add(name)
            for name in row['Layers']:
                require(type(name) is str and re.fullmatch('blobs/sha256/[0-9a-f]{64}',name))
                used.add(name)
        # Direct root manifests are references, not ancillary legacy configs.
        for desc in index['manifests']:
            require(type(desc) is dict and desc.get('mediaType')=='application/vnd.oci.image.manifest.v1+json')
            name=blob_path(desc.get('digest'))
            used.add(name)
            manifest=read(name,digest=name.rsplit('/',1)[1])
            require(type(desc.get('size')) is int and entries[name].size==desc['size'])
            require(type(manifest) is dict and type(manifest.get('config')) is dict
                    and type(manifest.get('layers')) is list and len(manifest['layers'])<=256)
            used.add(blob_path(manifest['config'].get('digest')))
            for layer in manifest['layers']:
                require(type(layer) is dict)
                used.add(blob_path(layer.get('digest')))
        extra={n for n,m in entries.items() if m.isfile()}-used
        require(len(extra)<=256)
        result={'extra_blobs':len(extra),'extra_blob_hash_matches':0,
                'legacy_shape_nodes':0,'unknown_shape_nodes':0,'invalid_identity_nodes':0,
                'duplicate_ids':0,'roots':0,'parent_edges':0,'missing_parents':0,
                'cycle_nodes':0,'terminal_nodes':0,'terminal_shared_config_matches':0,
                'single_chain_shape':False,'diagnostic_only':True,
                'derivation_verified':False}
        nodes={}
        for name in extra:
            require(re.fullmatch('blobs/sha256/[0-9a-f]{64}',name))
            value=read(name,digest=name.rsplit('/',1)[1])
            result['extra_blob_hash_matches']+=1
            if not (type(value) is dict and set(value)<=FIELDS
                    and {'id','created','container_config','os'}<=set(value)
                    and type(value['container_config']) is dict and value['os']=='linux'):
                result['unknown_shape_nodes']+=1
                continue
            result['legacy_shape_nodes']+=1
            identity,parent=value['id'],value.get('parent','')
            if not (type(identity) is str and re.fullmatch('[0-9a-f]{64}',identity)
                    and type(parent) is str and (parent=='' or re.fullmatch('[0-9a-f]{64}',parent))):
                result['invalid_identity_nodes']+=1
                continue
            if identity in nodes:
                result['duplicate_ids']+=1
                continue
            nodes[identity]=value
        parents={v.get('parent','') for v in nodes.values()}-{''}
        result['roots']=sum(not v.get('parent') for v in nodes.values())
        result['parent_edges']=sum(bool(v.get('parent')) for v in nodes.values())
        result['missing_parents']=sum(v.get('parent','') not in nodes and bool(v.get('parent')) for v in nodes.values())
        cycles=set()
        for identity in nodes:
            chain=[]
            while identity in nodes and identity not in chain:
                chain.append(identity);identity=nodes[identity].get('parent','')
            if identity in chain:
                cycles.update(chain[chain.index(identity):])
        result['cycle_nodes']=len(cycles)
        terminals=[v for k,v in nodes.items() if k not in parents]
        result['terminal_nodes']=len(terminals)
        result['terminal_shared_config_matches']=sum(
            any(all(v.get(k)==c.get(k) for k in SHARED) for c in configs) for v in terminals)
        result['single_chain_shape']=(len(nodes)==len(extra)>0 and result['roots']==1
            and result['terminal_nodes']==1 and result['parent_edges']==len(nodes)-1
            and not any(result[k] for k in ('missing_parents','cycle_nodes','duplicate_ids','invalid_identity_nodes','unknown_shape_nodes')))
        return result
