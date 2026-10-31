import os, sys, subprocess, paramiko

EGO = r"C:\Users\Admin\.local\share\ego\active_version_dir\ego-browser.exe"
MJS = r"C:\Users\Admin\Documents\deepseek-harness\default-workspace\workbuddy2api-panel\_verify_qqbot.mjs"

c = paramiko.SSHClient(); c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect("ct-game.com", 22, "root", os.environ["SSHPW"], timeout=30)
i, o, e = c.exec_command("python3 -c \"import json;print(json.load(open('/opt/workbuddy2api/config.json'))['api_key'])\"")
key = o.read().decode().strip()
c.close()
if not key:
    print("NO KEY"); sys.exit(2)
print("[browser] key loaded (len=%d), launching ego-browser" % len(key))

env = dict(os.environ)
env["WB_KEY"] = key
env["PATH"] = os.path.dirname(EGO) + os.pathsep + env.get("PATH", "")

expr = "await import('file:///%s')" % MJS.replace("\\", "/")
p = subprocess.run([EGO, "nodejs", "-e", expr], env=env, capture_output=True, text=True, timeout=300)
print("[browser] rc=%d" % p.returncode)
print("--- stdout ---"); print(p.stdout)
print("--- stderr ---"); print(p.stderr[-3000:])
