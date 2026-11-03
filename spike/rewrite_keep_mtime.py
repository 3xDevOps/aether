import json,sys,time,os
f,a,e=sys.argv[1],sys.argv[2],float(sys.argv[3]); st=os.stat(f)
o={"accessToken":a,"expiresAt":int((time.time()+e)*1000),"scopes":["user:inference","user:profile"],"subscriptionType":"max"}
with open(f,"r+") as h: h.seek(0); h.truncate(); h.write(json.dumps({"claudeAiOauth":o}))
os.utime(f,ns=(st.st_atime_ns,st.st_mtime_ns)); print("rewrote in place keeping mtime",a,flush=True)
