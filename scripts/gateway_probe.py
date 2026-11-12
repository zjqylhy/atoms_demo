"""只读查看已授权网关的模型列表，密钥从不回显的标准输入读取。"""
import getpass
import json
import urllib.request

secret = getpass.getpass("Gateway API key (hidden): ")
request = urllib.request.Request(
    "https://api.linhengyi.xyz/v1/models",
    headers={"Authorization": "Bearer " + secret},
)
try:
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=20) as response:
        payload = json.load(response)
    print(json.dumps({"models": [m.get("id") for m in payload.get("data", [])]}, ensure_ascii=False))
except Exception as error:
    print(str(error).replace(secret, "[REDACTED]"))
    raise SystemExit(1)
finally:
    secret = ""
