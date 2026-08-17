import json
import sys


def load(path: str) -> dict:
    with open(path, encoding="utf-8") as source:
        return json.load(source)


def assert_common(document: dict) -> None:
    services = document["services"]
    assert set(services) == {"sazanami", "konomitv"}
    for service in services.values():
        assert service["network_mode"] == "host"
        assert service["user"] == "1000:1000"
        assert service["cap_drop"] == ["ALL"]
        assert "no-new-privileges:true" in service["security_opt"]
        assert service.get("privileged") is not True
        assert not service.get("devices")
        for volume in service["volumes"]:
            assert volume["source"] != "/"
            assert "docker.sock" not in volume["source"]
            assert volume["target"] != "/host-rootfs"
    assert services["sazanami"]["read_only"] is True
    assert services["sazanami"]["healthcheck"]["test"][-1] == "http://127.0.0.1:4521/api/recordings?limit=1"
    konomitv = services["konomitv"]
    assert konomitv["platform"] == "linux/amd64"
    assert konomitv["build"]["context"].endswith("#0a32188274b81c1e7bed642474b208bd2a543a6b")


def volume(document: dict, target: str) -> dict:
    return next(item for item in document["services"]["konomitv"]["volumes"] if item["target"] == target)


base = load(sys.argv[1])
deletion = load(sys.argv[2])
assert_common(base)
assert_common(deletion)
assert volume(base, "/host-rootfs/recordings")["read_only"] is True
assert volume(deletion, "/host-rootfs/recordings").get("read_only") is not True
lock = volume(deletion, "/host-rootfs/recordings/.sazanami-dvr.lock")
assert lock["read_only"] is True
assert lock["source"].endswith("/recordings/.sazanami-dvr.lock")
