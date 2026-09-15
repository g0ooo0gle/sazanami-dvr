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


def assert_konomitv_example(path: str) -> None:
    with open(path, encoding="utf-8") as source:
        lines = [line.rstrip("\n") for line in source]
    expected = "    always_receive_tv_from_mirakurun: true"
    assert lines.count(expected) == 1
    assert sum(line.strip().startswith("always_receive_tv_from_mirakurun:") for line in lines) == 1


if len(sys.argv) not in (3, 4):
    raise SystemExit("usage: compose_test.py <compose.json> <delete.json> [konomitv.yaml]")

base = load(sys.argv[1])
deletion = load(sys.argv[2])
assert_common(base)
assert_common(deletion)
if len(sys.argv) == 4:
    assert_konomitv_example(sys.argv[3])
assert volume(base, "/host-rootfs/recordings")["read_only"] is True
assert volume(deletion, "/host-rootfs/recordings").get("read_only") is not True
lock = volume(deletion, "/host-rootfs/recordings/.sazanami-dvr.lock")
assert lock["read_only"] is True
assert lock["source"].endswith("/recordings/.sazanami-dvr.lock")
