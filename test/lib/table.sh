# shellcheck shell=bash
# Helpers that read the `-o wide` tables the Registry views render.
#
# A cell is cut at the header's column offsets, never split on whitespace: a
# blank cell is only padding, so splitting on whitespace drops it and shifts
# every later cell one field to the left. The views pad by rune, so the offsets
# are counted in characters under a UTF-8 locale, not in bytes.

# _table_columns HEADER sets _table_start, _table_width and _table_name to the
# offset, width and header word of each column. The first column starts at the
# start of the line and the last one (width -1) runs to its end, so every
# character of a row belongs to one cell.
_table_columns() {
  local LC_ALL=C.UTF-8
  local header="$1" i k
  _table_start=() _table_width=() _table_name=()
  for ((i = 0; i < ${#header}; i++)); do
    [[ "${header:i:1}" != " " ]] || continue
    ((i == 0)) || [[ "${header:i-1:1}" == " " ]] || continue
    _table_start+=("$i")
    _table_name+=("${header:i}")
  done
  ((${#_table_start[@]} > 0)) || _table_start=(0) _table_name=("")
  _table_start[0]=0
  for ((k = 0; k < ${#_table_start[@]}; k++)); do
    _table_name[k]="${_table_name[k]%% *}"
    if ((k + 1 < ${#_table_start[@]})); then
      _table_width[k]=$((_table_start[k + 1] - _table_start[k]))
    else
      _table_width[k]=-1
    fi
  done
}

# _table_cut LINE sets _table_cell to LINE's cells, with the padding after each
# one trimmed. A blank cell stays an empty element in its own position.
_table_cut() {
  local LC_ALL=C.UTF-8
  local line="$1" cell k
  _table_cell=()
  for ((k = 0; k < ${#_table_start[@]}; k++)); do
    if ((_table_width[k] < 0)); then
      cell="${line:_table_start[k]}"
    else
      cell="${line:_table_start[k]:_table_width[k]}"
    fi
    _table_cell+=("${cell%"${cell##*[! ]}"}")
  done
}

# _table_index HEADER prints the index of the column headed HEADER, or -1.
_table_index() {
  local k
  for ((k = 0; k < ${#_table_name[@]}; k++)); do
    [[ "${_table_name[k]}" != "$1" ]] || {
      echo "$k"
      return
    }
  done
  echo -1
}

# table_without_age prints the table on stdin, header included, one line per
# row with its cells joined by a tab and the AGE column left out. The AGE
# column ticks between two calls; every other cell, blank or not, must match.
table_without_age() {
  local header line out sep k age
  IFS= read -r header || [[ -n "$header" ]] || return 0
  _table_columns "$header"
  age="$(_table_index AGE)"
  line="$header"
  while :; do
    _table_cut "$line"
    out="" sep=""
    for ((k = 0; k < ${#_table_cell[@]}; k++)); do
      ((k != age)) || continue
      out+="$sep${_table_cell[k]}"
      sep=$'\t'
    done
    printf '%s\n' "$out"
    IFS= read -r line || [[ -n "$line" ]] || break
  done
}

# table_column TABLE HEADER prints each row's cell in the column headed HEADER,
# one per line; it prints nothing when no column is headed HEADER.
table_column() {
  local header line col
  { IFS= read -r header || [[ -n "$header" ]]; } <<<"$1"
  _table_columns "$header"
  col="$(_table_index "$2")"
  ((col >= 0)) || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    _table_cut "$line"
    printf '%s\n' "${_table_cell[col]}"
  done < <(sed 1d <<<"$1")
}

# table_row TABLE NAME prints the first row of TABLE whose NAME cell is NAME,
# or nothing.
table_row() {
  local header line col
  { IFS= read -r header || [[ -n "$header" ]]; } <<<"$1"
  _table_columns "$header"
  col="$(_table_index NAME)"
  ((col >= 0)) || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    _table_cut "$line"
    if [[ "${_table_cell[col]}" == "$2" ]]; then
      printf '%s\n' "$line"
      return
    fi
  done < <(sed 1d <<<"$1")
}
