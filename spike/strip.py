import re,sys
d=open(sys.argv[1],'rb').read().decode('utf8','replace')
d=re.sub(r'\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]|\x1b[=>]','',d)
print(re.sub(r'\n\s*\n+','\n',d)[-int(sys.argv[2]) if len(sys.argv)>2 else -4000:])
