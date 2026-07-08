# Perch

Remote terminal do PowerShell 7 na Windows, dla klienta CLI na Linuksie.
Odpowiednik SSH dla jednego konkretnego scenariusza: serwer musi widzieć
fizyczny desktop (GUI, DPAPI) zalogowanego użytkownika — czego standardowy
OpenSSH server na Windows nie zapewnia. Zobacz `remote-pwsh-terminal-spec.md`
po pełny kontekst i uzasadnienie.

**Bezpieczeństwo:** brak TLS i uwierzytelniania — świadoma decyzja dla małej,
w pełni zaufanej sieci LAN. Jedyną linią obrony jest firewall ograniczający
ruch do podsieci LAN (patrz niżej). Nie wystawiaj tego portu do internetu.

## Instalacja (gotowe binarki)

Przy każdym tagu `vX.Y.Z` GitHub Actions buduje wszystkie artefakty i
publikuje je jako [GitHub Release](../../releases/latest):
`perch-server.exe` (windows/amd64), `perch-386` i `perch-amd64` (klient
linux), `perch-windows-amd64.exe` (klient windows), plus `checksums.txt`.

**Uwaga:** protokół nie ma negocjacji wersji. Niezgodna para klient/serwer
(np. stary serwer + nowy klient) rozłącza się od razu z niejasnym błędem
typu `expected RESIZE as first frame, got 9` — jeśli to widzisz, zaktualizuj
oba binarki do tego samego tagu z Releases.

**Klient na Linuksie — jedna linia:**

```bash
curl -sSL https://raw.githubusercontent.com/Luunoronti/Perch/main/install.sh | sh
```

(skrypt wykrywa architekturę amd64/386, weryfikuje sumę SHA-256 i instaluje
do `~/.local/bin/perch`).

**Serwer na Windows:** pobierz `perch-server.exe` z release'a ręcznie — to
pojedynczy plik, bez instalatora. Zobacz sekcję "Uruchamianie serwera"
niżej, jak go poprawnie odpalić.

**Klient na Windows:** pobierz `perch-windows-amd64.exe` ręcznie z release'a
i uruchom z PowerShell/cmd — działanie identyczne jak na Linuksie (raw mode,
resize, sesje trwałe), tylko wykrywanie zmiany rozmiaru terminala działa
przez polling zamiast `SIGWINCH` (Windows nie ma takiego sygnału).

## Build ze źródeł

```bash
make            # server + client (linux/386, linux/amd64, windows/amd64)
make server     # tylko serwer
make client     # tylko klient (wszystkie trzy warianty)
```

Artefakty trafiają do `dist/` (`perch-server.exe`, `perch-386`,
`perch-amd64`, `perch-windows-amd64.exe`).

## Uruchamianie serwera — WYMAGANIE

`perch-server.exe` **musi** działać w interaktywnej sesji desktopowej
zalogowanego użytkownika Windows, nie jako usługa. To jest cały powód
istnienia tego projektu (GUI + DPAPI działają tylko wtedy).

**Poprawnie:**
- Skrót do `perch-server.exe` w folderze Autostart (`shell:startup`).
- Albo Harmonogram zadań z wyzwalaczem "Przy logowaniu" i opcją
  **"Uruchom tylko, gdy użytkownik jest zalogowany"**.

**Zabronione:** rejestracja jako usługa Windows lub Harmonogram z "Uruchom
niezależnie od tego, czy użytkownik jest zalogowany" — to Session 0, GUI i
DPAPI nie zadziałają. Serwer wypisze ostrzeżenie na starcie, jeśli wykryje,
że nie jest w aktywnej sesji konsolowej, ale nie zablokuje się na twardo.

Przy pierwszym uruchomieniu serwer tworzy config w
`%APPDATA%\perch\server.json` (domyślnie `listen: 0.0.0.0:2222`, `shell` —
autodetekcja `pwsh.exe`).

### Firewall

```powershell
New-NetFirewallRule -DisplayName "Perch" -Direction Inbound -Action Allow `
  -Protocol TCP -LocalPort 2222 -RemoteAddress LocalSubnet
```

## Uruchamianie klienta

```bash
perch -server 192.168.1.50:2222
```

Żeby nie podawać adresu za każdym razem, zapisz go raz jako domyślny:

```bash
perch -default-server 192.168.1.50:2222   # zapisuje do configu i kończy działanie
perch                                     # dalej łączy się z zapisanym adresem
```

Config klienta: `~/.config/perch/client.json` (`server: "host:port"`).
Flaga `-server` nadpisuje adres tylko na czas jednego uruchomienia, bez
zapisywania.

Ctrl-C, Ctrl-Z, strzałki, kolory ANSI — wszystko leci surowo do `pwsh`
(raw mode terminala), tak jak w SSH. Terminal jest zawsze przywracany do
normalnego trybu przy wyjściu (`exit` w pwsh albo zerwanie połączenia).

## Sesje trwałe (persistent sessions)

Domyślnie (bez `-session`) każde połączenie dostaje własny, jednorazowy
`pwsh` — ginie razem z połączeniem, dokładnie jak SSH.

Z `-session <nazwa>` dostajesz **trwałą** sesję: serwer trzyma ją żywą (ze
wszystkimi zmiennymi, katalogiem roboczym, uruchomionymi procesami) nawet
po rozłączeniu klienta, aż do `exit` wewnątrz powłoki. Możesz się rozłączyć
z maszyny A i wznowić dokładnie tam, gdzie skończyłeś, z maszyny B:

```bash
# maszyna A
perch -server 192.168.1.50:2222 -session praca
# ... coś robisz, zamykasz terminal / tracisz sieć ...

# maszyna B, później
perch -server 192.168.1.50:2222 -session praca   # ta sama sesja, ten sam stan
```

Uwaga: jeśli dwóch klientów podłączy się do tej samej nazwy **jednocześnie**,
oboje widzą to samo wyjście i oboje mogą pisać — bez blokad ani wskazywania,
kto właśnie pisze (patrz spec §6.3, poza zakresem MVP). Do sekwencyjnego
"przenoszenia się" między maszynami działa to bez zarzutu.
