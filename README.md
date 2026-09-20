# Sazanami DVR

Mirakurun / mirakcとKonomiTVをつなぐ、軽量なLinux録画バックエンドです。

## 目次

- [概要](#概要)
- [必要なもの](#必要なもの)
- [Linux](#linux)
- [Compose](#compose)
- [KonomiTV](#konomitv)
- [対応範囲](#対応範囲)
- [詳しい文書](#詳しい文書)

## 概要

Sazanami DVRは、番組表、予約、録画、録画済み番組、ライブ視聴に必要な機能を提供します。
KonomiTVからはEDCB互換バックエンドとして接続し、放送ストリームはMirakurun互換APIから取得します。

現在の安定版はv1.3.2です。配布ファイルは[GitHub Releases](https://github.com/g0ooo0gle/sazanami-dvr/releases)で確認できます。

## 必要なもの

- x86_64またはarm64のLinux
- チューナーとチャンネルを設定済みのMirakurunまたはmirakc
- 直接導入ではsystemdとroot権限
- Compose導入ではDocker EngineとDocker Compose
- KonomiTVを使う場合はKonomiTV v0.14.1
- ソースからビルドする場合だけGo 1.26.6

チューナー、ドライバー、カード、Mirakurun / mirakcは本製品に含みません。

## Linux

配布アーカイブのインストーラで、systemd環境へ導入できます。DBやチャンネルは明示的に準備してからサービスを起動します。

[Linuxへインストールする](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/docs/getting-started/linux.md)

## Compose

Sazanami DVRとKonomiTVを同じLinuxホストで起動する最小構成を用意しています。

[Docker Composeで起動する](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/docs/getting-started/docker-compose.md)

## KonomiTV

KonomiTVのバックエンドをEDCBに設定し、CtrlCmdの接続先をSazanami DVRへ向けます。

[KonomiTVを接続する](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/docs/getting-started/konomitv.md)

## 対応範囲

- GR / BS / CS 2KのMPEG-TS
- 番組表、通常予約、自動予約、予約の変更と取消し
- 放送途中からの予約、録画中の停止、部分録画
- 完成録画の一覧・再生、Range取得、ライブ中継
- 同じPCだけで使う運用WebUI

KonomiTV v0.14.1の主要な録画操作を対象にしています。EDCB全体や、KonomiTV・Komorebiの全機能との互換性を示すものではありません。
CtrlCmdには認証とTLSがないため、インターネットへ公開せず、信頼できるLANだけで使用してください。

## 詳しい文書

導入後の操作、設定、更新、復旧、トラブル対応は[ドキュメント一覧](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/docs/README.md)から探せます。

開発への参加は[CONTRIBUTING.md](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/CONTRIBUTING.md)、脆弱性の連絡は[SECURITY.md](https://github.com/g0ooo0gle/sazanami-dvr/blob/v1.3.2/SECURITY.md)を参照してください。

## License

[MIT License](LICENSE)および[第三者ライセンス表示](THIRD_PARTY_NOTICES.md)を参照してください。
