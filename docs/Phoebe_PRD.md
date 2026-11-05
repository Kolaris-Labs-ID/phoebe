# Phoebe Product Requirements Document

> **Panduan untuk AI agent**
> - Gunakan dokumen ini sebagai spesifikasi produk Phoebe MVP versi 0.1.
> - Bedakan keputusan yang sudah ditetapkan dari usulan yang masih perlu divalidasi.
> - P0 wajib untuk MVP; P1 dan fitur di luar MVP tidak termasuk scope implementasi awal.
> - Pertahankan pemisahan transkrip, catatan manual, notes AI, dan MoM beserta revision sumbernya.
> - Gunakan acceptance criteria dan definition of done untuk verifikasi. Dokumen ini tidak menetapkan harga subscription atau model final.

Versi Markdown ini mempertahankan isi PRD Word. Panduan di atas membantu pembacaan oleh agent dan tidak menambah requirement produk.

MVP aplikasi iOS untuk rekaman meeting dan notulen yang dapat diedit

Kolaris Labs  |  Versi 0.1  |  6 November 2026

### Ringkasan produk

Phoebe membantu pengguna merekam meeting, mengorganisasi rekaman, menghasilkan transkrip dan catatan, lalu menyusun Minutes of Meeting atau MoM yang dapat ditinjau, diedit, dan dibagikan. Hasil dapat diekspor sebagai PDF, Word DOCX, Markdown, atau teks ringkas untuk WhatsApp.

Dokumen ini menjadi acuan product, design, iOS, backend, dan QA untuk membangun MVP. Prinsip utama: rekaman harus tersimpan dengan andal, keputusan harus sesuai sumber, dan hasil edit pengguna harus terlindungi.

### Keputusan dan status

| Sudah ditetapkan | Usulan baseline untuk validasi |
| --- | --- |
| Nama produk Phoebe; dikembangkan oleh Kolaris Labs. | Bahasa awal Indonesia dan Inggris; pengguna individual terlebih dahulu. |
| Aplikasi iOS native dengan Swift; backend Go. | SwiftUI, PostgreSQL, private object storage, dan pemrosesan asinkron. |
| Record, organize, transcript, notes, editable MoM, export dan share. | Transkripsi melalui API; DeepSeek Flash kandidat LLM default; Gemini kandidat pembanding. |
| Produk SaaS. | Kuota berdasarkan menit audio; nominal harga dan paket belum ditetapkan. |

### Outcome yang dituju

Pengguna menyelesaikan satu meeting, mendapatkan draft MoM yang dapat dipercaya, mengoreksinya, dan membagikan hasil tanpa menyusun ulang dokumen di aplikasi lain.

Prioritas P0 berarti wajib untuk MVP. P1 berarti setelah alur utama stabil. Semua target performa, batas penggunaan, dan kebijakan retensi dalam dokumen ini adalah target produk yang perlu divalidasi sebelum peluncuran.

## 1 Tujuan dan batas produk

### Masalah pengguna

- Mencatat sambil meeting mengurangi perhatian dan sering melewatkan keputusan, tugas, atau detail penting.
- Rekaman panjang sulit ditelusuri; transkrip mentah belum siap dibagikan sebagai MoM.
- Menyalin catatan ke dokumen dan WhatsApp menambah pekerjaan setelah meeting.

### Pengguna awal

| Persona | Kebutuhan utama |
| --- | --- |
| Founder dan manager | Mencatat keputusan dan tindak lanjut rapat internal atau klien. |
| Project dan product lead | Mengubah diskusi requirement menjadi ringkasan dan daftar pekerjaan. |
| Konsultan dan profesional | Menghasilkan notulen yang rapi dan siap dibagikan. |

### P0 yang termasuk MVP

Recording mikrofon dan impor audio; folder dan tag; pencarian metadata serta teks yang sudah tersedia; transkrip bertimestamp; catatan manual dan AI; MoM terstruktur; editor dan versi; PDF, DOCX, Markdown, teks; share sheet; akun, sinkronisasi, kuota, dan deletion. Subscription menjadi syarat untuk peluncuran berbayar, bukan syarat pilot internal.

### Batasan platform

Pengambilan audio melalui mikrofon iPhone untuk meeting tatap muka atau suara dari perangkat lain. MVP tidak menjanjikan perekaman audio internal Zoom, WhatsApp Call, FaceTime, atau telepon dari aplikasi lain di iPhone yang sama. Panggilan dapat menginterupsi sesi recording; aplikasi wajib menangani interupsi secara eksplisit.

### Di luar MVP

Live transcription; bot meeting; macOS dan Android; Google Calendar; Google Docs native; chat lintas meeting; integrasi CRM atau task tracker; kolaborasi realtime; assignment tugas kepada akun lain; pengiriman WhatsApp otomatis; pengenalan identitas pembicara otomatis; self-hosted model.

### Prinsip kualitas

AI membuat draft, pengguna menentukan versi final. Jangan mengarang PIC, deadline, peserta, angka, atau keputusan. Informasi yang tidak tersedia diberi label belum ditentukan atau tidak disebutkan. Klaim dari catatan pribadi harus tetap dibedakan dari fakta dalam transkrip.

## 2 Alur pengguna dan layar

### Alur recording

Library meeting > New meeting > isi judul opsional > informasi recording dan izin mikrofon > Record > tambahkan catatan opsional > Pause atau Resume > Stop > simpan lokal > upload > transkrip > notes > Generate MoM > edit > tandai sudah ditinjau > export dan share.

### Alur impor

Import audio > pilih file > validasi tipe, durasi, ukuran, dan kuota > salin ke penyimpanan aplikasi > upload > gunakan pipeline transkrip dan dokumen yang sama. Impor file tidak meminta izin mikrofon.

### Alur tanpa internet

Pengguna yang sudah login dapat merekam dan menulis catatan tanpa koneksi. Audio serta draft tersimpan lokal. Aplikasi menampilkan Waiting for connection; upload dilanjutkan saat kondisi memungkinkan. Generasi AI dan sinkronisasi membutuhkan internet.

| Layar | Isi dan aksi utama |
| --- | --- |
| Onboarding dan akun | Login; penjelasan mikrofon dan pemrosesan cloud; pengaturan bahasa. |
| Library meeting | Daftar, cari, filter folder atau tag, import, new meeting, status proses. |
| Recording | Timer, indikator recording, pause, resume, stop, catatan singkat, peringatan interupsi. |
| Meeting detail | Audio playback serta tab Transcript, Notes, MoM; metadata dan processing status. |
| Editor MoM | Edit bagian, peserta, keputusan, action items, versi, status review, export. |
| Export dan share | Format, preview, opsi teks ringkas, native share sheet. |
| Settings | Pemakaian kuota, subscription, bahasa, retensi, account deletion. |

### Perilaku yang terlihat pengguna

- Setiap tahap menampilkan status dan tindakan yang jelas; gagal transkripsi tidak ditampilkan sebagai rekaman hilang.
- Pengguna boleh membaca transkrip meskipun generasi notes gagal, dan mengedit notes meskipun MoM gagal.
- Share draft diperbolehkan dengan label Draft; menandai sudah ditinjau tidak mengirim dokumen kepada siapa pun.

## 3 Kebutuhan recording dan impor

### REC-01 Recording yang andal

Start hanya aktif setelah izin mikrofon diberikan dan sesi audio berhasil dimulai. Pengguna dapat pause, resume, dan stop; durasi menghitung audio aktif. Rekaman lokal disimpan bertahap dalam segmen yang dapat dipulihkan. Kegagalan atau penutupan paksa tidak boleh membuat segmen yang sudah selesai hilang.

Recording aktif mendukung layar terkunci dan background dengan konfigurasi audio yang sesuai. Tidak ada janji recording terus berjalan setelah force quit. Saat aplikasi dibuka kembali, tawarkan pemulihan segmen yang valid; jangan menyalakan mikrofon otomatis.

### REC-02 Interupsi dan kondisi perangkat

Pantau panggilan masuk, perubahan audio route, akses mikrofon, dan kapasitas penyimpanan. Saat terinterupsi, tutup segmen yang valid dan tampilkan Paused by interruption. Resume dilakukan pengguna setelah sesi audio dapat diaktifkan. Jika penyimpanan tidak cukup, hentikan dengan aman dan pertahankan bagian yang tersimpan.

### REC-03 Catatan selama meeting

Pengguna dapat mengetik poin singkat sambil recording. Autosave lokal berlaku tanpa internet. Catatan manual tetap disimpan sebagai sumber terpisah dan menjadi petunjuk prioritas AI, bukan bukti bahwa sesuatu telah disepakati.

### IMP-01 Format dan batas awal

| Parameter | Baseline usulan |
| --- | --- |
| Output recording | AAC dalam M4A; kualitas dikalibrasi untuk percakapan dan konsumsi storage. |
| Input impor | M4A, MP3, WAV; validasi isi file, bukan ekstensi saja. |
| Durasi maksimum | 120 menit audio per meeting; durasi lebih panjang ditolak dengan pesan yang jelas. |
| Ukuran impor | Maksimum 250 MB; file besar tidak dimuat seluruhnya ke memori. |
| Kuota recording | Pengguna tetap dapat menyimpan lokal; pemrosesan cloud menunggu kuota cukup. |

### Kriteria penerimaan

- Rekam 60 menit dengan layar terkunci: file dapat diputar penuh dan tidak kehilangan segmen yang sudah tersimpan.
- Panggilan masuk menghasilkan status interupsi dan audio sebelumnya tetap dapat dipulihkan.
- Koneksi terputus tidak menghentikan recording lokal; upload dapat dilanjutkan tanpa membuat meeting duplikat.
- File rusak, kosong, tanpa suara yang bermakna, atau melampaui batas menghasilkan pesan spesifik; tidak menghasilkan MoM fiktif.

## 4 Transkripsi dan generasi AI

### TRN-01 Transkrip sebagai sumber

Setelah upload diverifikasi, worker memproses audio menjadi segmen teks dengan waktu mulai dan selesai. Bahasa dapat otomatis atau dipilih Indonesia dan Inggris. Provider wajib mendukung timestamp; jika tidak, gunakan chunk audio dengan offset waktu yang dapat diuji. Pemilihan model bergantung pada kualitas dan dukungan fitur ini.

Simpan output transkrip asli sebagai revision sumber. Koreksi pengguna membuat revision baru, sehingga teks asli tetap tersedia. Segmen dapat diketuk untuk memutar audio pada timestamp terkait. Jangan menetapkan nama pembicara berdasarkan tebakan; speaker label generik hanya jika didukung dan tervalidasi. Jika audio sudah dihapus atau kedaluwarsa, teks tetap tersedia tetapi playback dinonaktifkan dengan penjelasan.

### AI-01 Notes dan MoM

Pipeline: audio > transkrip tervalidasi > notes AI > MoM draft. Notes dibuat otomatis setelah transkrip siap. MoM dibuat atas tindakan pengguna dari versi notes yang dipilih. LLM menerima transkrip dan catatan manual; generasi MoM tetap memiliki akses ke bukti transkrip agar detail tidak hilang akibat ringkasan berantai.

Output disimpan sebagai struktur yang tervalidasi: bagian, keputusan, action items, dan referensi segmen sumber. Detail keputusan dan action item harus memiliki referensi segmen atau label berasal dari catatan pengguna. Referensi tidak ditampilkan sebagai bukti terverifikasi jika timestamp tidak valid.

### AI-02 Aturan hasil

- Pisahkan keputusan, usulan, pertanyaan terbuka, dan tindak lanjut. Deadline relatif hanya dinormalisasi bila tanggal dan zona waktu meeting jelas; simpan frasa aslinya.
- PIC atau tanggal yang tidak disebutkan bernilai kosong dengan label belum ditentukan. Kutipan harus sesuai sumber.
- Teks transkrip adalah data; instruksi yang muncul di percakapan tidak boleh mengubah aturan sistem atau memicu aksi eksternal.
- Bahasa output default mengikuti pilihan pengguna. Token, versi prompt, model, provider, dan latency dicatat tanpa memasukkan isi meeting ke log operasional.

### AI-03 Model dan kegagalan

DeepSeek Flash merupakan kandidat default untuk teks; Gemini Flash kandidat pembanding. Provider speech-to-text dipilih terpisah. Gunakan adapter pada backend dan konfigurasi model, bukan nama model yang ditanam di iOS. Model final ditentukan setelah evaluasi meeting Indonesia dan campuran Inggris.

Kegagalan API atau JSON invalid dapat diulang maksimum dua kali setelah percobaan awal. Tidak ada fallback lintas provider yang tersembunyi: perpindahan harus sesuai pengungkapan pemrosesan data pengguna. Input panjang dipotong pada batas segmen dan digabung dengan deduplikasi, tanpa membuang akhir meeting diam-diam.

## 5 Editor notes dan MoM

### DOC-01 Pemisahan dokumen dan versi

Catatan manual, notes AI, transkrip, dan MoM merupakan artefak terpisah. Setiap versi hasil generasi mencatat revision sumber. Mengedit MoM tidak mengubah transkrip. Jika sumber berubah, hasil lama diberi label Based on earlier version; tidak diganti otomatis.

Regenerate membuat versi baru. Versi hasil edit pengguna tetap tersedia, dengan pilihan membuka atau memulihkan versi tersebut. Pemulihan membuat revision baru. Pembaruan dari perangkat lain menggunakan pemeriksaan revision untuk mencegah overwrite diam-diam.

### DOC-02 Struktur default MoM

| Bagian | Isi dan perilaku |
| --- | --- |
| Informasi meeting | Judul, tanggal, zona waktu, peserta, dan agenda; peserta dapat diisi manual. |
| Ringkasan | Gambaran singkat tujuan dan hasil meeting. |
| Pembahasan | Poin per agenda atau topik dengan detail yang relevan. |
| Keputusan | Hanya kesepakatan yang didukung sumber; tampilkan referensinya. |
| Action items | Deskripsi tugas, PIC, deadline, dan referensi; field boleh belum ditentukan. |
| Hal terbuka | Pertanyaan, blocker, dan pembahasan yang belum selesai. |
| Tindak lanjut | Rencana pertemuan berikutnya hanya bila disebutkan atau diisi pengguna. |

### DOC-03 Kemampuan editor

Edit teks, heading, bullet, numbered list, dan action items; tambah atau hapus bagian; undo saat sesi aktif; autosave lokal maksimal dua detik setelah jeda mengetik. Sinkronkan ketika online. Navigasi keluar saat sinkronisasi tertunda menampilkan bahwa draft sudah tersimpan lokal.

Status review: Draft atau Reviewed. Penandaan Reviewed dilakukan pengguna dan bukan tanda tangan atau persetujuan peserta lain. Perubahan isi setelah Reviewed mengembalikan status menjadi Draft. Catatan manusia dan tambahan AI memiliki indikator asal yang terbaca tanpa bergantung pada warna.

### Kriteria penerimaan

- Pengguna mengoreksi keputusan, keluar, dan membuka kembali meeting: perubahan tetap ada.
- Regenerate tidak menimpa edit; pengguna dapat kembali ke versi sebelumnya.
- Perubahan transkrip menandai notes dan MoM yang terdampak sebagai versi lama.
- Dokumen tetap dapat diedit jika AI gagal atau kuota audio habis.

## 6 Organisasi ekspor dan sharing

### ORG-01 Library meeting

Pengguna dapat mengubah judul, tanggal, dan peserta; membuat folder satu tingkat; memindahkan meeting; menambahkan beberapa tag; mengurutkan berdasarkan tanggal terbaru; serta mencari judul, tag, dan teks transkrip atau notes atau MoM yang sudah tersinkronisasi. Pencarian cloud hanya mengambil data pemilik akun; tidak mencakup meeting pengguna lain.

### EXP-01 Format ekspor

| Format | Persyaratan hasil |
| --- | --- |
| PDF | Layout rapi dengan heading, metadata, keputusan dan action items; teks dapat dipilih; page break tidak memotong teks. |
| Word DOCX | Dokumen dapat diedit; heading dan list native; action items disajikan sebagai tabel atau list konsisten. |
| Markdown MD | UTF-8; heading dan list konsisten; dapat dibuka di editor teks tanpa markup yang rusak. |
| Teks WhatsApp | Ringkasan singkat, keputusan dan action items; preview dapat disalin atau dibagikan melalui share sheet. |

Interpretasi Docs pada MVP adalah DOCX. Membuat Google Docs native memerlukan integrasi tersendiri dan berada di fase berikutnya. Pilihan ekspor adalah notes atau MoM; audio dan transkrip tidak ikut dibagikan kecuali dipilih secara eksplisit pada fitur berikutnya.

### EXP-02 Konsistensi dan privasi

Semua format dibuat secara deterministik dari revision dokumen yang sama, tanpa panggilan LLM baru. Ekspor memuat judul, tanggal meeting, dan label Draft bila belum ditinjau. Gunakan snapshot revision saat ekspor sehingga edit yang berlangsung tidak menghasilkan isi campuran. Referensi internal dirender sebagai timestamp teks; tidak boleh membocorkan URL object storage atau token akses.

### SHR-01 Share melalui iOS

Native share sheet menerima file PDF, DOCX, MD atau teks. Pengguna memilih aplikasi tujuan dan penerima sendiri. WhatsApp yang terpasang dapat menerima format yang didukungnya; dukungan harus diuji pada perangkat nyata. Bila tujuan tidak tersedia, pengguna tetap dapat Save to Files atau Copy text. Membatalkan share tidak mengubah dokumen atau memicu pengiriman ulang.

### Kriteria penerimaan

- Keputusan, nominal, PIC dan deadline identik dalam PDF, DOCX dan Markdown dari revision yang sama.
- File PDF dan DOCX dibuka pada aplikasi umum tanpa teks terpotong atau karakter Indonesia yang rusak.
- WhatsApp diuji untuk berbagi dokumen dan teks; aplikasi tidak mengklaim pesan terkirim berdasarkan share sheet dibuka.

## 7 Arsitektur data dan pemrosesan

### Komponen teknis

iOS: Swift dan SwiftUI, AVFoundation, persistence lokal, upload file background, serta native share sheet. Backend: satu aplikasi Go modular dan proses worker terpisah. PostgreSQL menyimpan metadata dan dokumen; private object storage menyimpan audio dan ekspor. Durable job queue dapat memakai PostgreSQL pada MVP agar komponen operasional tetap sedikit.

### Objek data utama

| Entitas | Data pokok |
| --- | --- |
| User dan entitlement | Identitas, bahasa, timezone, paket, periode kuota dan status subscription. |
| Meeting dan audio asset | Owner, judul, tanggal, peserta manual, folder, tag, durasi, checksum dan status upload. |
| Transcript revision | Teks per segmen, timestamp, revision asli atau koreksi, provider dan bahasa. |
| Document revision | Jenis manual notes atau AI notes atau MoM, struktur isi, sumber, review status. |
| Processing job | Tahap, attempt, idempotency key, status, error category dan source revision. |
| Usage dan export | Reservation atau konsumsi menit, token per panggilan, format dan document revision. |

### State dan pemulihan

Recording lokal: idle > recording > paused atau interrupted > stopped > queued for upload. Processing cloud: uploading > uploaded > transcribing > transcript ready > generating notes > notes ready. MoM memiliki job terpisah: requested > generating > draft ready. Gagal terjadi per tahap, bukan mengganti seluruh meeting menjadi tidak bisa diakses.

Upload menggunakan session ID, checksum, final verification dan dukungan resume sesuai storage. Start processing hanya setelah verifikasi selesai. API pembuatan job memakai idempotency key; retry atau callback duplikat tidak membuat versi atau usage charge ganda. Worker menulis hasil secara atomik dan memiliki timeout, lease, serta retry backoff.

### Kontrak API minimum

Autentikasi dan session; CRUD meeting dan folder atau tag; create serta complete upload; start transkripsi; baca status job; baca atau koreksi transkrip; create atau regenerate notes dan MoM; update document dengan expected revision; create export; usage serta entitlement; delete audio, meeting dan akun. Semua akses diverifikasi terhadap owner pada server.

### Penyelesaian saat aplikasi ditutup

Pemrosesan cloud tetap berjalan setelah upload selesai walau aplikasi ditutup. Saat dibuka kembali, aplikasi mengambil status terbaru. Push completion bersifat P1; polling dengan backoff cukup untuk MVP. Ketergantungan upload background harus diuji pada lock screen, suspend, force quit dan jaringan berpindah.

## 8 Akun monetisasi dan privasi

### ACC-01 Akun dan sinkronisasi

Baseline login: Sign in with Apple; metode tambahan P1. Token disimpan di Keychain. Data cloud terikat user ID internal, bukan alamat email sebagai kunci utama. Tidak ada akses tim atau public meeting link pada MVP. Pengguna dapat bekerja pada draft lokal yang tersimpan ketika offline; sinkronisasi memakai revision dan penanganan konflik yang jelas.

### BIL-01 Kuota SaaS

Paket dan harga belum diputuskan. Infrastruktur mendukung trial, paket bulanan, dan tambahan kuota pada fase berikutnya. Trial dan batas menit harus dapat dikonfigurasi di backend. Kuota cloud ditahan sebelum transkripsi, dikonsumsi sekali ketika transkrip berhasil, dan dilepas jika proses gagal permanen. Tagihan produk berbeda dari biaya provider yang mungkin tetap muncul saat attempt gagal.

Durasi dihitung dari audio tervalidasi oleh server; pause tidak dihitung. Ledger menyimpan detik dan antarmuka menampilkan menit. Jangan membulatkan setiap segmen menjadi satu menit. Koreksi, ekspor, dan retry sistem tidak mengonsumsi kuota audio lagi. Regenerate AI memiliki rate limit terpisah agar biaya terkendali.

Recording lokal tidak diblokir saat kuota habis. Pengguna diberi tahu bahwa transkripsi menunggu upgrade atau periode berikutnya. Untuk peluncuran berbayar di iOS, implementasikan StoreKit 2 dan verifikasi entitlement pada server, restore purchases, perubahan paket, expiry dan refund; detail komersial mengikuti review kebijakan distribusi yang berlaku.

### PRV-01 Kontrol data

- Sebelum recording, jelaskan bahwa audio akan diproses di cloud dan pengguna perlu memberi tahu peserta. Izin mikrofon iOS bukan persetujuan peserta meeting.
- TLS untuk transfer, enkripsi storage at rest, private bucket, URL akses berumur pendek, secret provider hanya pada backend. Jangan menyebut end-to-end encryption untuk pipeline cloud ini.
- Audio lokal dipertahankan sampai upload terverifikasi; penghapusan lokal setelahnya hanya sesuai preferensi pengguna. Default usulan audio cloud 30 hari; notes dan MoM sampai dihapus. Kebijakan akhir harus terlihat sebelum upload.
- Delete audio tidak menghapus notes. Delete meeting menghapus seluruh turunannya dan membatalkan job. Worker harus memeriksa tombstone sebelum menulis agar data tidak muncul kembali.
- Delete akun mencabut akses dan menjadwalkan penghapusan data aktif maksimal 30 hari; backup kedaluwarsa maksimal 90 hari sebagai target awal. Batas provider dan backup harus sesuai kebijakan yang benar-benar dapat dipenuhi.
- Isi audio, transkrip dan notes tidak masuk analytics atau log debug. Penggunaan data untuk training internal tidak termasuk MVP; ketentuan provider harus diperiksa sebelum produksi.

## 9 Kualitas observabilitas dan acceptance

### Target nonfungsional awal

| Area | Target dan cara verifikasi |
| --- | --- |
| Reliability | Tidak kehilangan segmen recording yang sudah selesai pada uji interupsi, lock dan force quit; recovery tidak mengklaim bagian yang belum tersimpan. |
| Processing | 95% audio 60 menit menjadi notes dalam 15 menit setelah upload terverifikasi pada pilot load 10 job paralel; ukur per tahap. |
| Editor dan library | Autosave lokal maksimal 2 detik setelah jeda; library 1.000 meeting dapat dicari metadata dalam 2 detik pada perangkat uji yang disepakati. |
| Security | Tidak ada kebocoran lintas akun pada seluruh endpoint, file, search dan job; uji expired URL dan auth revocation. |
| Accessibility | Dynamic Type, VoiceOver pada kontrol utama; status recording tidak disampaikan melalui warna saja. |
| Compatibility | Usulan minimum iOS 17; uji perangkat fisik versi minimum serta versi iOS terbaru saat rilis. |

### Evaluasi AI sebelum model dipilih

Gunakan minimal 30 meeting berizin: Indonesia, Inggris dan campuran; ruangan tenang dan bising; percakapan angka, nama dan istilah teknis. Dua reviewer menilai berdasarkan transkrip rujukan. Bandingkan model dengan prompt dan sumber yang sama; catat biaya aktual, durasi, validitas struktur, dan waktu koreksi pengguna.

Gate pilot: tidak ada PIC, nominal, deadline atau keputusan fiktif pada set evaluasi; minimal 90% keputusan dan action items penting dari anotasi reviewer tercakup. Semua kegagalan struktur AI ditangani sebelum dokumen tersedia. Kegagalan gate memerlukan perbaikan pipeline, bukan sekadar mengganti label hasil.

### Skenario acceptance wajib

Recording offline lalu upload; lock screen 60 menit; panggilan masuk; storage penuh; file rusak; kuota habis; dua retry job yang sama; provider timeout; transkrip salah dan dikoreksi; regenerate setelah edit; konflik revision; share draft atau cancel; delete saat job aktif; subscription expired; akses meeting akun lain ditolak.

### Observabilitas

Pantau processing success, queue age, retry rate, durasi tahap, biaya per jam audio, error recording, crash, dan export failure. Alert pada antrean macet serta lonjakan biaya. Correlation ID menghubungkan meeting dan job tanpa mengirim isi percakapan ke analytics.

## 10 Delivery ukuran sukses dan keputusan

### Tahap delivery dan exit criteria

| Tahap | Hasil yang harus terbukti |
| --- | --- |
| A Recording spike | Recording, interupsi, recovery dan upload pada perangkat fisik; batas iOS dan format timestamp provider tervalidasi. |
| B Pipeline inti | Akun, library, impor, transkrip, notes serta state dan retry; tidak ada charge atau job duplikat. |
| C MoM dan sharing | Editor, revision, source references, PDF, DOCX, MD, teks WhatsApp; semua format konsisten. |
| D Pilot | Evaluasi 30 meeting, pengukuran biaya aktual, privacy deletion, kualitas dan performa lolos. |
| E Peluncuran berbayar | Entitlement, purchase restore, kuota, kebijakan data dan review distribusi selesai; harga ditetapkan. |

Tanggal delivery ditetapkan setelah tahap A dan konfirmasi kapasitas tim. Product owner: Ari Rudiana. PIC iOS, backend, design dan QA ditetapkan saat kickoff; dokumen ini tidak menetapkan anggota tim tanpa konfirmasi.

### Ukuran sukses pilot yang diusulkan

- Activation minimal 60%: pengguna baru menghasilkan lalu mengedit atau menandai Reviewed satu MoM dalam tujuh hari; hitung terhadap pengguna pilot yang menyelesaikan onboarding.
- Processing success minimal 95%: meeting audio valid yang diterima menghasilkan notes tanpa intervensi admin, dalam retry budget.
- Median waktu edit MoM maksimal lima menit, diukur pada sesi review pilot; durasi terbuka saja tidak dianggap waktu kerja aktif.
- Minimal 40% MoM yang ditinjau menghasilkan export sukses atau share sheet dibuka dalam tujuh hari. Ini indikator intent, bukan bukti pesan terkirim.

### Event minimum

onboarding_completed, recording_started, recording_interrupted, recording_saved, upload_verified, transcription_completed, notes_ready, mom_generated, document_saved, mom_reviewed, export_completed, share_sheet_opened, quota_blocked. Deduplikasi dengan event ID dan ukur tanpa merekam isi meeting.

### Keputusan sebelum kickoff atau rilis

Konfirmasi minimum iOS; durasi dan ukuran maksimum; format impor; provider transkripsi bertimestamp; model dan budget thinking; retensi audio dan backup; region pemrosesan; harga, trial dan kuota; minimum perangkat uji. Pemilihan provider dan retensi selesai sebelum pilot eksternal; paket dan billing selesai sebelum rilis berbayar.

## 11 Acuan implementasi dan keputusan lanjutan

### Acuan teknis

Referensi berikut mendukung batas platform dan evaluasi provider. PRD tidak bergantung pada harga atau nama model tertentu; periksa dokumentasi dan harga saat implementasi serta sebelum rilis.

Apple AVAudioSession setActive dan batas microphone saat panggilan  
<https://developer.apple.com/documentation/avfaudio/avaudiosession/setactive(_:options:)>

Apple kategori record dan background recording  
<https://developer.apple.com/documentation/avfaudio/avaudiosession/category-swift.struct/record>

Apple UIActivityViewController untuk native share sheet  
<https://developer.apple.com/documentation/uikit/uiactivityviewcontroller>

DeepSeek model dan harga API untuk kandidat pemrosesan teks  
<https://api-docs.deepseek.com/quick_start/pricing/>

Google Gemini model dan harga API untuk kandidat pembanding  
<https://ai.google.dev/gemini-api/docs/pricing>

OpenAI harga transkripsi untuk evaluasi biaya speech to text  
<https://developers.openai.com/api/docs/pricing>

### Catatan pengembangan berikutnya

P1 setelah MVP stabil: pembicara generik dan pemetaan nama manual, beberapa template MoM, push completion, kalender, dan audio import dari share extension. Setiap penambahan harus mempertahankan kontrol revision, privasi, dan biaya per jam audio.

Fase lanjutan: workspace tim dan permission, Google Docs native, pencarian semantik lintas meeting, meeting chat, integrasi task tracker, serta aplikasi macOS untuk pengalaman meeting desktop. Setiap fase membutuhkan PRD tambahan; tidak dimasukkan ke estimasi MVP.

### Definition of done

Alur recording atau impor hingga share berhasil pada perangkat fisik; seluruh P0 dan skenario acceptance lulus; hasil evaluasi AI terdokumentasi; risiko kehilangan audio dan akses lintas akun terselesaikan; biaya aktual dan batas pemakaian terukur; proses deletion berjalan; serta tidak ada keputusan provider, retensi atau komersial yang belum selesai untuk tahap peluncuran terkait.
