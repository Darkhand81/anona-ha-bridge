"""Extract embedded protobuf FileDescriptors from jadx output and print .proto text."""
import sys; sys.path.insert(0, "/tmp/claude-1000/-home-paul/a9dda853-227c-41c0-b045-d99554c7ee6b/scratchpad/tools/pylib")
import re, sys, pathlib
from google.protobuf import descriptor_pb2 as dp

LIT = re.compile(r'"((?:[^"\\]|\\.)*)"')
def unescape(s):
    out, i = [], 0
    while i < len(s):
        c = s[i]
        if c != '\\': out.append(c); i += 1; continue
        n = s[i+1]
        if n == 'u': out.append(chr(int(s[i+2:i+6], 16))); i += 6
        elif n in '01234567':
            j = i+1
            while j < len(s) and j < i+4 and s[j] in '01234567': j += 1
            out.append(chr(int(s[i+1:j], 8))); i = j
        else: out.append({'n':'\n','t':'\t','r':'\r','b':'\b','f':'\f','0':'\0'}.get(n, n)); i += 2
    return ''.join(out)

LBL = {1:'optional ',2:'required ',3:'repeated '}
TYP = {v:k[5:].lower() for k,v in dp.FieldDescriptorProto.Type.items()}
def msg(m, ind):
    p = '  '*ind; o = [f'{p}message {m.name} {{']
    for e in m.enum_type: o += enum(e, ind+1)
    for n in m.nested_type: o += msg(n, ind+1)
    for f in m.field:
        t = f.type_name.lstrip('.') if f.type_name else TYP[f.type]
        oo = f' /*oneof {m.oneof_decl[f.oneof_index].name}*/' if f.HasField('oneof_index') else ''
        o.append(f'{p}  {LBL.get(f.label,"")}{t} {f.name} = {f.number};{oo}')
    return o + [f'{p}}}']
def enum(e, ind):
    p = '  '*ind
    return [f'{p}enum {e.name} {{'] + [f'{p}  {v.name} = {v.number};' for v in e.value] + [f'{p}}}']

for path in sys.argv[2:]:
    src = pathlib.Path(path).read_text(errors='replace')
    k = src.find('internalBuildGeneratedFileFrom(new java.lang.String[]{')
    if k < 0: continue
    pos, parts = src.index('{', k) + 1, []
    while True:
        lm = re.compile(r'\s*"((?:[^"\\]|\\.)*)"\s*(,?)').match(src, pos)
        if not lm: break
        parts.append(unescape(lm.group(1))); pos = lm.end()
    data = ''.join(parts).encode('latin-1')
    fd = dp.FileDescriptorProto.FromString(data)
    out = [f'// {fd.name}  package={fd.package}  deps={list(fd.dependency)}']
    for e in fd.enum_type: out += enum(e, 0)
    for mm in fd.message_type: out += msg(mm, 0)
    pathlib.Path(sys.argv[1], fd.name).write_text('\n'.join(out) + '\n')
    print(fd.name, len(fd.message_type), 'msgs')
