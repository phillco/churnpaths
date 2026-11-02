complete -c churnpaths -f
complete -c churnpaths -s h -l help -d 'Show help'
complete -c churnpaths -s s -l source -r -F -d "Churn list"
complete -c churnpaths -s H -l hostname -x -d 'Host selector'
complete -c churnpaths -s m -l mechanism -x -a 'backblaze timemachine spotlight' -d 'Consumer'
complete -c churnpaths -s j -l json -d 'JSON output'
complete -c churnpaths -s l -l list -d 'Resolve folders only'
complete -c churnpaths -l state -r -F -d 'Spotlight ownership state'
complete -c churnpaths -s a -l apply -d 'Apply Spotlight privacy changes'
complete -c churnpaths -s c -l check -d 'Check Spotlight policy drift'
