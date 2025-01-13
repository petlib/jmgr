.PHONY: testclean
testclean:
	@echo "clean up after tests" 
	jmgr destroy -r -f testJ99
	

# run all tests, 
.PHONY: test
test: test1 test2 test3 test4 test5 test6 test7 test8 test9 testLast

# test show config
.PHONY: test1
test1:
	@echo "test1 config" 
	/usr/local/bin/jmgr config

# test create jail
.PHONY: test2
test2:
	@echo "test2 create jail testJ99" 
	/usr/local/bin/jmgr create -f testJ99

.PHONY: test3
test3:
	@echo "test3 update patch testJ99" 
	/usr/local/bin/jmgr update -f patch testJ99

.PHONY: test4
test4:
	@echo "test4 start jail testJ99" 
	/usr/local/bin/jmgr start testJ99

.PHONY: test5
test5:
	@echo "test5 update pkgs testJ99" 
	/usr/local/bin/jmgr update -f pkgs testJ99

.PHONY: test6
test6:
	@echo "test6 stop jail testJ99" 
	/usr/local/bin/jmgr stop testJ99

.PHONY: test7
test7:
	@echo "test7 enable jail testJ99" 
	/usr/local/bin/jmgr enable testJ99

# test show jails
.PHONY: test8
test8:
	@echo "test8 jmgr testJ99" 
	/usr/local/bin/jmgr testJ99

# test show jails
.PHONY: test9
test9:
	@echo "test9 disable testJ99" 
	/usr/local/bin/jmgr disable testJ99

.PHONY: testLast
testLast:
	@echo "All tests completed. Test jail testJ99 created." 
	@echo "Play with testJ99 then remove the jail with command: jmgr destroy -r testJ99"


