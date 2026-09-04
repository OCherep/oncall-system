

## Відновлення крипто / скриптів зі stash

```bash
cd /opt/ops/oncall
git stash list
git stash show --stat stash@{0}
# відновити лише скрипт
git checkout stash@{0} -- scripts/install-zerossl.sh
# або весь stash
git stash pop stash@{0}
```

Приватні ключі (`privkey.pem`, `private.key`) **не повинні** бути в git/stash.
Відновлення ключа = re-issue в ZeroSSL/LE + `bash scripts/install-zerossl.sh ./certs/s.ks.tv`.
