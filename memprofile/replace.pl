#!/usr/bin/perl
# replace.pl TARGET OLD_FILE NEW_FILE [EXPECTED_COUNT]
# Literal, whole-file replacement of a multi-line block. Fails loudly if the
# block is not found exactly EXPECTED_COUNT times, so a patch can never apply
# to a source that has drifted.
use strict; use warnings;
my ($target, $oldf, $newf, $want) = @ARGV;
$want = 1 unless defined $want;
local $/;
open(my $t, '<', $target) or die "open $target: $!";  my $s = <$t>; close $t;
open(my $o, '<', $oldf)   or die "open $oldf: $!";    my $old = <$o>; close $o;
open(my $n, '<', $newf)   or die "open $newf: $!";    my $new = <$n>; close $n;
$old =~ s/\n\z//; $new =~ s/\n\z//;
my $count = () = $s =~ /\Q$old\E/g;
die "FAILED: found $count occurrences in $target, wanted $want\n" unless $count == $want;
$s =~ s/\Q$old\E/$new/g;
open($t, '>', $target) or die; print $t $s; close $t;
print "patched $target ($count site(s))\n";
