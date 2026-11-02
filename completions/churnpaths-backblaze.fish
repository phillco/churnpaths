complete -c churnpaths-backblaze -f
complete -c churnpaths-backblaze -s h -l help -d 'Show help'
complete -c churnpaths-backblaze -s s -l source -r -F -d 'Churn list'
complete -c churnpaths-backblaze -s H -l hostname -x -d 'Host selector'
complete -c churnpaths-backblaze -s x -l xml -r -F -d 'Editable exclusions file'
complete -c churnpaths-backblaze -l state-dir -r -a '(__fish_complete_directories)' -d 'Backups and change log'
complete -c churnpaths-backblaze -s j -l json -d 'JSON output'
complete -c churnpaths-backblaze -s a -l apply -d 'Write changes'
complete -c churnpaths-backblaze -s c -l check -d 'Exit 1 if the file differs'
