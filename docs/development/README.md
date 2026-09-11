[README](../../README.md) / [ドキュメント](../README.md) / 開発者向け

# 開発者向けドキュメント

実装、テスト、設計判断を調べるための入口です。セットアップ手順は[利用者向けドキュメント](../README.md)にあります。

## 開発情報

- [アーキテクチャ](architecture.md)
- [設計判断](decisions.md)
- [テスト](testing.md)
- [依存関係](dependencies.md)

## コードの入口

- CLI: [`cmd/sazanami-dvr`](../../cmd/sazanami-dvr)
- ドメイン: [`internal/core`](../../internal/core)
- アプリケーション: [`internal/app`](../../internal/app)
- 外部接続: [`internal/adapters`](../../internal/adapters)
- パッケージング: [`packaging`](../../packaging)

## 主な設計資料

- [公開文書の情報設計](../adr/0071-public-documentation-information-architecture.md)
- [KonomiTV向けバックエンドの目標](../adr/0067-konomitv-practical-backend-goal.md)
- [Compose構成](../../spec/operations/docker-compose-v1.md)
- [公開文書仕様](../../spec/operations/public-documentation-v1.md)

ADR、仕様、計画書は既存の場所で管理します。開発の流れは[CONTRIBUTING.md](../../CONTRIBUTING.md)を参照してください。
