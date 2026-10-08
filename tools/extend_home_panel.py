"""Extend the pinned Home frontend's own navigation and router at build time.

The upstream image contains compiled frontend assets, not frontend source. Match
its exact checksum and unique anchors so an upstream change fails the build
instead of silently producing an incompatible navigation or router.
"""
import hashlib
from pathlib import Path
import sys

ENTRY = 'assets/js/management.dbbb9e9e60.js'
SHA256 = '4159d86a83683e17a71bdf8485ccc37856696f670e34e8c6169a005dbf0e833f'
QUOTA_OLD = 'te=(0,N.un)({getParentRoute:()=>e5,path:"/admin/quota",validateSearch:e=>({q:"string"==typeof e.q&&e.q.trim()?e.q.trim():void 0}),component:function(){let e=te.useSearch();return(0,n.jsx)(P.C,{to:"/admin/upstream",search:{tab:"accounts",q:e.q},replace:!0})}})'
QUOTA_NEW = 'te=(0,N.un)({getParentRoute:()=>e5,path:"/admin/quota",component:homeQuotaPage})'
QUOTA_COMPONENT = '''function homeQuotaPage(){
const connectedAt=(0,I.B)(state=>state.connectedAt);
const managementKey=(0,I.B)(state=>state.managementKey);
return(0,n.jsx)("iframe",{key:connectedAt+":"+Boolean(managementKey),src:"/quota-panel.html",title:"Subscription quotas",style:{border:0,width:"100%",height:"100%",display:"block"}})
}'''
CONSOLE_ROUTE = 'homeConsoleRoute=(0,N.un)({getParentRoute:()=>e5,path:"/admin/api-console",component:function(){r.useEffect(()=>{window.location.replace("/console.html#/")},[]);return null}}),'


def replace_once(source, before, after):
    if source.count(before) != 1:
        raise ValueError(f'Expected exactly one pinned Home anchor: {before[:70]}')
    return source.replace(before, after, 1)


STRATEGY_CHUNKS = {
    '1118.1319e9ef2d.js': '5508201500c6c07d482f5f1fbf1b76430cc0f2584f9bb0501f5cf774f2a980ec',
    '416.bc50b27cbd.js': 'fe546b5a65cfc6ad1f908e45dd456cd4d0c7156864d2513ca6f846d069d60d81',
    '8063.3b6b38f7b2.js': '3bdc0dcc8b34f2aa702c3f48cbf1d5bb20aa6cbb92464edf72050a21ffd30f2c',
}


def extend_strategies(directory, entry):
    for filename, checksum in STRATEGY_CHUNKS.items():
        raw = (directory / 'assets/js' / filename).read_bytes()
        if hashlib.sha256(raw).hexdigest() != checksum:
            raise ValueError('Pinned Home strategy chunk changed: ' + filename)
        source = replace_once(raw.decode(), 'case"fill-first":case"fillfirst":case"ff":return"fill-first";default:return"unknown"',
            'case"fill-first":case"fillfirst":case"ff":return"fill-first";case"quota-reset":return"quota-reset";case"weighted-round-robin":case"weightedroundrobin":case"wrr":return"weighted-round-robin";default:return"unknown"')
        if filename.startswith('8063.'):
            source = replace_once(source, 'options:["round-robin","fill-first"]', 'options:["round-robin","fill-first","weighted-round-robin","quota-reset"]')
            source = replace_once(source, 'o(`configPage.routingStrategies.${e}`)', 'e==="quota-reset"?"Quota reset priority":e==="weighted-round-robin"?"Weighted round robin":o(`configPage.routingStrategies.${e}`)')
        chunk_id, old_hash, _ = filename.split('.')
        new_hash = hashlib.sha256(source.encode()).hexdigest()[:10]
        (directory / 'assets/js' / f'{chunk_id}.{new_hash}.js').write_text(source)
        entry = replace_once(entry, f'{chunk_id}:"{old_hash}"', f'{chunk_id}:"{new_hash}"')
    return entry


def extend(directory):
    directory = Path(directory)
    raw = (directory / ENTRY).read_bytes()
    if hashlib.sha256(raw).hexdigest() != SHA256:
        raise ValueError('Home frontend changed; review integration before updating the pinned image')
    source = extend_strategies(directory, raw.decode())
    source = replace_once(source, 'items:[{to:"/admin/usage",', 'items:[{to:"/admin/quota",labelKey:"Quotas",metaKey:"Subscription capacity and reset times",icon:ee.A},{to:"/admin/usage",')
    source = replace_once(source, '{to:"/admin/system-nodes",labelKey:', '{to:"/admin/api-console",labelKey:"API Console",metaKey:"Open CLI Proxy API Console",icon:er.A},{to:"/admin/system-nodes",labelKey:')
    source = replace_once(source, QUOTA_OLD, QUOTA_NEW)
    source = replace_once(source, 'tu=e5.addChildren([e4,', CONSOLE_ROUTE + 'tu=e5.addChildren([homeConsoleRoute,e4,')
    source = replace_once(source, 'var eU=a(45234);', QUOTA_COMPONENT + 'var eU=a(45234);')
    # Preserve the native shell byte-for-byte. Only its menu data and routes change.
    shell_start, shell_end = 'function eT(e){', 'let eW=new Set('
    assert source.split(shell_start)[1].split(shell_end)[0] == raw.decode().split(shell_start)[1].split(shell_end)[0]
    name = 'assets/js/management.home-' + hashlib.sha256(source.encode()).hexdigest()[:12] + '.js'
    html = (directory / 'management.html').read_text()
    html = replace_once(html, '/' + ENTRY, '/' + name)
    (directory / name).write_text(source)
    (directory / 'management.html').write_text(html)
    print('Extended native Home sidebar and routes:', name)


if __name__ == '__main__':
    extend(sys.argv[1])
