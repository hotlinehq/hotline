import re,os,sys,json,xml.etree.ElementTree as ET
root=sys.argv[1]; out=sys.argv[2]
ents=re.findall(r'<!ENTITY\s+\S+\s+SYSTEM\s+"([^"]+)"',open(f"{root}/xmlconf.xml",encoding='utf-8').read())
cases=[];skipped={}
def skip(why): skipped[why]=skipped.get(why,0)+1
for e in ents:
    p=f"{root}/{e}"
    if not os.path.exists(p): continue
    txt=open(p,'rb').read()
    # the collection files define entities for sub-files? parse as is; strip DOCTYPE internal subset
    t=re.sub(rb'<!DOCTYPE[^\[>]*(\[.*?\])?\s*>',b'',txt,flags=re.S)
    try: tree=ET.fromstring(t)
    except Exception:
        try: tree=ET.fromstring(b'<R>'+re.sub(rb'^<\?xml[^>]*\?>',b'',t.strip())+b'</R>')
        except Exception as ex: print("collection parse failed",e,ex); continue
    def walk(node,base):
        b=node.get('{http://www.w3.org/XML/1998/namespace}base') or ''
        base=os.path.join(base,b) if b else base
        for ch in node:
            if ch.tag=='TEST':
                f=os.path.normpath(os.path.join(os.path.dirname(p),base,ch.get('URI')))
                cases.append(dict(id=ch.get('ID'),type=ch.get('TYPE'),ents=ch.get('ENTITIES') or 'none',version=ch.get('VERSION') or '1.0',edition=ch.get('EDITION'),ns=ch.get('NAMESPACE'),path=f,sec=ch.get('SECTIONS'),coll=e))
            else: walk(ch,base)
    walk(tree,'')
keep=[]
for c in cases:
    if c['version'] not in('1.0',): skip('xml 1.1'); continue
    if c['edition'] and '5' not in c['edition'].split(): skip('older XML edition rules'); continue
    if 'namespaces' in c['coll'] and c['type'] in ('not-wf','error'): skip('namespace rules (ignored on purpose)'); continue
    if c['ents']!='none': skip('needs entity declarations'); continue
    try: d=open(c['path'],'rb').read()
    except Exception: skip('missing file'); continue
    if d[:2] in(b'\xff\xfe',b'\xfe\xff') or d[:4] in (b'\x00\x00\x00<',b'<\x00\x00\x00',b'\x00<\x00?',b'<\x00?\x00'): skip('utf-16/32'); continue
    m=re.match(rb'^\xef\xbb\xbf?\s*<\?xml[^>]*encoding\s*=\s*["\']([^"\']+)',d)
    if m and m.group(1).lower().replace(b'-',b'') not in (b'utf8',): skip('non-utf-8 encoding'); continue
    if b'<!DOCTYPE' in d: skip('has DOCTYPE (refused on purpose)'); continue
    try: d.decode('utf-8')
    except Exception: skip('not utf-8'); continue
    c['expect']='notwf' if c['type']=='not-wf' else 'wf'
    keep.append(c)
json.dump(keep,open(out,'w'))
print(len(cases),'tests in suite;',len(keep),'kept:',sum(1 for k in keep if k['expect']=='wf'),'well-formed,',sum(1 for k in keep if k['expect']=='notwf'),'not well-formed')
for k,v in sorted(skipped.items(),key=lambda x:-x[1]): print('  skipped',v,k)
