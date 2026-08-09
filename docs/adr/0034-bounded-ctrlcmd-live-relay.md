# ADR-0034: CtrlCmdライブ視聴はMirakurun streamへ上限付きで中継する

- Status: Accepted
- Date: 2026-08-09
- Decision owner: Project owner
- Delegated reviewer: Codex
- Authorization: Project ownerはv1.0.0までの技術判断、実験環境での視聴、private LAN内の待受をCodexへ委任した
- Related: ADR-0026、Plan 0040、Plan 0047、Handoff 0022

## 背景

Sazanami DVR v0.0.9は番組表、予約、録画、完成録画の直接再生に対応したが、CtrlCmd 1073、301、1074を実装していない。このため、KonomiTV v0.14.1の既定ライブ視聴と、Komorebi 1.1.0-beta6のEDCB直接ライブを開始できない。

両クライアントは、1073で放送局とNetworkTV IDを指定し、返されたprocess IDを301へ渡す。301の成功応答後は、同じTCP接続からMPEG-TSを読み続ける。終了時は1074へNetworkTV IDを送る。

## 判断

1073、301、1074を録画常駐processのCtrlCmd adapterへ追加する。チャンネルは完成済みスナップショットで照合し、301を受けた時点でMirakurun／mirakcの復号済みservice streamを一度だけ開く。SazanamiはDVBやチューナーを直接制御しない。

ライブ利用はメモリだけで管理する。NetworkTV IDとprocess IDを対応付け、終了要求、接続断、提供元切断、取消し、process停止で解放する。DB schemaは変更しない。

同時ライブは4本、1073から301までの待ち時間は30秒、1接続は12時間を上限とする。録画用とライブ用のMirakurun stream adapterを分け、ライブ視聴が録画の同時実行枠を消費しないようにする。

301だけを長時間CtrlCmd接続として扱う。通常commandの14秒上限、CtrlCmd接続数、handler数、要求本文上限は維持する。301の成功headerは提供元streamを開いた後にだけ送り、TSは固定bufferで逐次転送する。

1074は未知または終了済みのNetworkTV IDにも成功を返す。これは両クライアントが開始前の掃除として1074を送るためであり、未確認の開始操作を成功扱いにする判断ではない。

## 結果

- KonomiTVとKomorebiで共通のライブ視聴経路を利用できる。
- 録画とライブが同じProvider portを使いながら、資源上限を独立して管理できる。
- 再起動時に復元できない接続状態をDBへ残さずに済む。
- 信頼できるLANでは認証なしのCtrlCmdからライブ映像へ到達できるため、インターネットへ直接公開してはならない。
- 画質変換、HLS、クライアント画面の実機確認は別の証拠として扱う。

## 採用しなかった案

- **直接チューナー制御:** Mirakurun／mirakcの責任を重複実装し、障害点を増やすため不採用。
- **1073でstreamを開く:** 301が来ない利用で提供元の枠を占有するため不採用。
- **ライブ状態のDB永続化:** TCP接続を再起動後に復元できず、古い状態の掃除だけが増えるため不採用。
- **録画と同じ同時数を共有:** ライブ視聴が録画開始を妨げるため不採用。
- **無期限接続:** 切断を検出できない接続が資源を占有し続けるため不採用。
