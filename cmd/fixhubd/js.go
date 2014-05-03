package main

const scriptText = `
function goproblems() {
	var path = document.forms[0].repoText.value;
	window.location = window.location.origin + "/" + path;
	return false;
}

function goconfirm() {
	if (document.getElementById("donotshowagain").checked) {
		document.cookie="skipconfirm=true; expires=Fri, 4 Dec 2020 12:00:00 GMT";
	}
}
`
