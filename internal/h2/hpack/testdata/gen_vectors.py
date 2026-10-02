import re
lines=open('rfc7541.txt').read().split('\n')
secs=[]; cur=None; mode=None
for l in lines:
    m=re.match(r'^(C\.[2-6]\.[0-9])\.\s',l)
    if m:
        cur={'id':m.group(1),'hex':'','list':[],'size':None}; secs.append(cur); mode=None; continue
    if cur is None: continue
    s=l.strip()
    if s.startswith('Header list to encode:'): mode='list'; continue
    if s.startswith('Hex dump of encoded data:'): mode='hex'; continue
    if s.startswith('Decoding process:'): mode=None; continue
    if s.startswith('Decoded header list:'): mode=None; continue
    if re.match(r'^(RFC 7541|Peon & Ruellan)',l): continue
    m=re.match(r'^\s*Table size:\s*(\d+)',l)
    if m: cur['size']=int(m.group(1)); continue
    if mode=='hex':
        m=re.match(r'^   ([0-9a-f ]+?)\s*\|',l)
        if m: cur['hex']+=m.group(1).replace(' ','')
    elif mode=='list' and s:
        cur['list'].append(s)
print('// Code generated bởi testdata/gen_vectors.py từ rfc7541.txt (rfc-editor.org, tải 2026-10-01) — Appendix C.')
print('package hpack\n')
print('type rfcVector struct {\n\tid    string\n\thex   string\n\tlist  []string // "name: value" đúng như RFC in\n\tsize  int      // Table size sau khi decode\n}\n')
print('var rfcVectors = []rfcVector{')
for c in secs:
    print('\t{id: %r, hex: %r, list: []string{%s}, size: %d},'%(c['id'],c['hex'],', '.join('%s'%__import__('json').dumps(x) for x in c['list']),c['size'] or 0))
print('}')
