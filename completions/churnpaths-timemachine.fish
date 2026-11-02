complete -c churnpaths-timemachine -f
complete -c churnpaths-timemachine -s h -l help -d 'Show help'
complete -c churnpaths-timemachine -s s -l source -r -F -d 'Churn list'
complete -c churnpaths-timemachine -s H -l hostname -x -d 'Host selector'
complete -c churnpaths-timemachine -s c -l check -d 'Exit 1 if any folder is still included'
complete -c churnpaths-timemachine -n __fish_use_subcommand -a apply -d 'Exclude resolved folders'
complete -c churnpaths-timemachine -n __fish_use_subcommand -a status -d 'Show exclusion state'
complete -c churnpaths-timemachine -n '__fish_seen_subcommand_from apply' -s n -l dry-run -d 'Preview only'
