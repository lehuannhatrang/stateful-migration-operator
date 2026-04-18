"""Launch an ipython kernel with migration-aware reconnection support.

This is a modified version of the Jupyter Enterprise Gateway kernel launcher that
adds CRIU checkpoint/restore awareness. After a CRIU restore, the kernel process
resumes but JEG has lost the connection info. This launcher registers a SIGUSR1
handler that re-sends the connection info to JEG when signaled by the migration
agent (daemonset).

Original source:
  https://github.com/jupyter-server/enterprise_gateway/blob/main/etc/kernel-launchers/python/scripts/launch_ipykernel.py
"""

import argparse
import base64
import json
import logging
import os
import random
import signal
import socket
import tempfile
import time
import uuid
from multiprocessing import Process
from threading import Thread

from Cryptodome.Cipher import AES, PKCS1_v1_5
from Cryptodome.PublicKey import RSA
from Cryptodome.Random import get_random_bytes
from Cryptodome.Util.Padding import pad
from jupyter_client.connect import write_connection_file

LAUNCHER_VERSION = 1  # Indicate to server the version of this launcher (payloads may vary)

# Minimum port range size and max retries, let EG_ env values act as the default for b/c purposes
min_port_range_size = int(
    os.getenv("MIN_PORT_RANGE_SIZE", os.getenv("EG_MIN_PORT_RANGE_SIZE", "1000"))
)
max_port_range_retries = int(
    os.getenv("MAX_PORT_RANGE_RETRIES", os.getenv("EG_MAX_PORT_RANGE_RETRIES", "5"))
)

log_level = os.getenv("LOG_LEVEL", os.getenv("EG_LOG_LEVEL", "10"))
log_level = int(log_level) if log_level.isdigit() else log_level

logging.basicConfig(format="[%(levelname)1.1s %(asctime)s.%(msecs).03d %(name)s] %(message)s")

logger = logging.getLogger("launch_ipykernel")
logger.setLevel(log_level)

DEFAULT_KERNEL_CLASS_NAME = "ipykernel.ipkernel.IPythonKernel"
__spark_context = None

# ---------------------------------------------------------------------------
# Migration-aware reconnection state
# ---------------------------------------------------------------------------
# _migration_params is populated before start_ipython() and serves as the
# fallback for reconnection.  start_ipython(locals()) aggressively deletes
# every non-dunder name from the module namespace, so all reconnection logic
# is encapsulated in _MigrationHandler whose instance survives because the
# signal subsystem holds a reference to its bound method.
_migration_params = {}

KERNEL_PID_FILE = "/tmp/.eg_kernel_launcher.pid"
RECONNECT_RESPONSE_ADDR_OVERRIDE = "/tmp/.eg_reconnect_response_addr"
RECONNECT_MAX_RETRIES = int(os.getenv("EG_RECONNECT_MAX_RETRIES", "10"))
RECONNECT_BASE_DELAY = float(os.getenv("EG_RECONNECT_BASE_DELAY", "2"))


class _MigrationHandler:
    """Self-contained handler for post-CRIU-restore reconnection.

    ``start_ipython(locals())`` wipes all module-level names that don't start
    with ``__``.  Every standard-library module, the ``_encrypt`` helper,
    ``logger``, and the ``_migration_params`` dict all disappear from the
    module namespace.

    This class captures the values it needs at construction time and
    re-imports standard-library modules inside each method so it remains
    fully functional after the namespace cleanup.

    Env-var resolution order (highest priority first):
      1. ``/tmp/.eg_reconnect_response_addr`` file  (response_addr only)
      2. Container PID 1 environment (``/proc/1/environ``)
      3. Current process environment (``/proc/self/environ``)
      4. Process environment    (``os.environ`` – stale after CRIU restore)
      5. ``_migration_params``  (values stored at initial launch)

    For the response address, both ``EG_RESPONSE_ADDRESS`` and
    ``RESPONSE_ADDRESS`` are checked at each layer.
    """

    ENV_KERNEL_ID = "KERNEL_ID"
    ENV_PUBLIC_KEY = "PUBLIC_KEY"
    ENV_RESPONSE_ADDRESS = "EG_RESPONSE_ADDRESS"
    ENV_RESPONSE_ADDRESS_ALT = "RESPONSE_ADDRESS"
    RESPONSE_ADDR_OVERRIDE_PATH = RECONNECT_RESPONSE_ADDR_OVERRIDE

    def __init__(self, params, max_retries, base_delay):
        self._params = params
        self._max_retries = max_retries
        self._base_delay = base_delay

    # -- signal entry point ------------------------------------------------

    def signal_handler(self, signum, frame):
        """SIGUSR1 handler – schedules an async re-send of connection info."""
        import logging
        from threading import Thread

        logging.getLogger("launch_ipykernel").info(
            "Received signal %d – scheduling connection info re-send", signum
        )
        Thread(target=self._resend_connection_info, daemon=True).start()

    # -- environment helpers -----------------------------------------------

    @staticmethod
    def _read_proc_environ(path):
        """Read a ``/proc/*/environ`` file and return it as a dict.

        After CRIU restore ``os.environ`` still holds the checkpointed
        values.  The container runtime (CRI-O) may have injected different
        env vars into the restored container via the OCI spec.
        ``/proc/1/environ`` reflects those actual values.
        """
        env = {}
        try:
            with open(path, "rb") as f:
                data = f.read()
            for item in data.split(b"\0"):
                if b"=" in item:
                    key, _, value = item.partition(b"=")
                    env[key.decode("utf-8", errors="replace")] = value.decode(
                        "utf-8", errors="replace"
                    )
        except Exception:
            pass
        return env

    def _get_response_addr(self, env, source_label):
        """Return the response address from *env* dict, checking both
        ``EG_RESPONSE_ADDRESS`` and ``RESPONSE_ADDRESS``.  Returns the
        value and the key that matched, or ``(None, None)``.
        """
        import logging

        log = logging.getLogger("launch_ipykernel")
        for key in (self.ENV_RESPONSE_ADDRESS, self.ENV_RESPONSE_ADDRESS_ALT):
            val = env.get(key)
            if val:
                log.debug(
                    "Found response address in %s: %s=%s", source_label, key, val
                )
                return val, key
        return None, None

    def _resolve_params(self):
        """Build effective params with updated env vars after CRIU restore.

        Resolution order (last write wins):
          1. ``_migration_params`` (values stored at initial launch)
          2. Process environment (``os.environ`` - may be stale post-CRIU)
          3. ``/proc/self/environ`` (current process env from the kernel)
          4. ``/proc/1/environ`` (container PID 1 - authoritative)
          5. ``/tmp/.eg_reconnect_response_addr`` file override
        """
        import logging
        import os

        log = logging.getLogger("launch_ipykernel")
        params = dict(self._params)
        log.debug(
            "Resolve params – stored response_addr=%s",
            params.get("response_addr", "<unset>"),
        )

        # layer: process env (os.environ – may be stale post-CRIU)
        if os.environ.get(self.ENV_KERNEL_ID):
            params["kernel_id"] = os.environ[self.ENV_KERNEL_ID]
        if os.environ.get(self.ENV_PUBLIC_KEY):
            params["public_key"] = os.environ[self.ENV_PUBLIC_KEY]
        addr, _key = self._get_response_addr(dict(os.environ), "os.environ")
        if addr:
            params["response_addr"] = addr

        # layer: /proc/self/environ (restored process – may differ from PID 1)
        self_env = self._read_proc_environ("/proc/self/environ")
        if self_env.get(self.ENV_KERNEL_ID):
            params["kernel_id"] = self_env[self.ENV_KERNEL_ID]
        if self_env.get(self.ENV_PUBLIC_KEY):
            params["public_key"] = self_env[self.ENV_PUBLIC_KEY]
        addr, _key = self._get_response_addr(self_env, "/proc/self/environ")
        if addr:
            params["response_addr"] = addr

        # layer: /proc/1/environ (container PID 1 – authoritative)
        container_env = self._read_proc_environ("/proc/1/environ")
        if container_env.get(self.ENV_KERNEL_ID):
            params["kernel_id"] = container_env[self.ENV_KERNEL_ID]
        if container_env.get(self.ENV_PUBLIC_KEY):
            params["public_key"] = container_env[self.ENV_PUBLIC_KEY]
        addr, _key = self._get_response_addr(container_env, "/proc/1/environ")
        if addr:
            params["response_addr"] = addr

        # layer: file override (response address only)
        if os.path.exists(self.RESPONSE_ADDR_OVERRIDE_PATH):
            try:
                with open(self.RESPONSE_ADDR_OVERRIDE_PATH) as f:
                    override = f.read().strip()
                if override:
                    log.debug(
                        "Found response address override in %s: %s",
                        self.RESPONSE_ADDR_OVERRIDE_PATH,
                        override,
                    )
                    params["response_addr"] = override
            except Exception:
                pass

        log.debug(
            "Resolve params – final response_addr=%s",
            params.get("response_addr", "<unset>"),
        )
        return params

    # -- encryption (duplicated to be self-contained) ----------------------

    @staticmethod
    def _encrypt(connection_info_str, public_key):
        """Encrypt connection info – standalone re-import of Cryptodome."""
        import base64
        import json

        from Cryptodome.Cipher import AES, PKCS1_v1_5
        from Cryptodome.PublicKey import RSA
        from Cryptodome.Random import get_random_bytes
        from Cryptodome.Util.Padding import pad

        aes_key = get_random_bytes(16)
        cipher = AES.new(aes_key, mode=AES.MODE_ECB)

        encrypted_connection_info = cipher.encrypt(pad(connection_info_str, 16))
        b64_connection_info = base64.b64encode(encrypted_connection_info)

        imported_public_key = RSA.importKey(base64.b64decode(public_key.encode()))
        cipher = PKCS1_v1_5.new(key=imported_public_key)
        encrypted_key = base64.b64encode(cipher.encrypt(aes_key))

        payload = {
            "version": 1,
            "key": encrypted_key.decode(),
            "conn_info": b64_connection_info.decode(),
        }
        return base64.b64encode(json.dumps(payload).encode(encoding="utf-8"))

    # -- resend logic ------------------------------------------------------

    def _do_resend(self, params, response_addr):
        """Send current kernel connection info to JEG at *response_addr*."""
        import json
        import logging
        import os
        import socket

        log = logging.getLogger("launch_ipykernel")

        parts = response_addr.split(":")
        if len(parts) != 2:
            raise ValueError(f"Invalid response address format: '{response_addr}'")

        response_ip = parts[0]
        response_port = int(parts[1])

        with open(params["connection_file"]) as fp:
            cf_json = json.load(fp)

        pid = os.getpid()
        cf_json["pid"] = pid
        cf_json["pgid"] = os.getpgid(pid)
        cf_json["comm_port"] = params["comm_port"]
        cf_json["kernel_id"] = params["kernel_id"]

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.settimeout(30)
            s.connect((response_ip, response_port))
            json_content = json.dumps(cf_json).encode(encoding="utf-8")
            log.debug("Resend JSON Payload '%s'", json_content)
            payload = self._encrypt(json_content, params["public_key"])
            log.debug("Resend Encrypted Payload '%s'", payload)
            s.send(payload)

    def _resend_connection_info(self):
        """Re-send with retry & exponential backoff."""
        import logging
        import time

        log = logging.getLogger("launch_ipykernel")

        if not self._params:
            log.warning("Migration params not initialised – cannot resend")
            return

        params = self._resolve_params()
        response_addr = params["response_addr"]

        log.info(
            "Reconnection parameters resolved – kernel_id=%s, response_addr=%s",
            params.get("kernel_id", "<unset>"),
            response_addr,
        )

        for attempt in range(1, self._max_retries + 1):
            try:
                self._do_resend(params, response_addr)
                log.info(
                    "Successfully re-sent connection info to JEG at %s (attempt %d)",
                    response_addr,
                    attempt,
                )
                return
            except Exception as e:
                delay = min(self._base_delay * (2 ** (attempt - 1)), 60)
                log.warning(
                    "Resend attempt %d/%d failed: %s – retrying in %.1fs",
                    attempt,
                    self._max_retries,
                    e,
                    delay,
                )
                time.sleep(delay)

        log.error(
            "Failed to re-send connection info after %d attempts", self._max_retries
        )


def _write_pid_file():
    """Persist the launcher PID so the migration agent can target the signal."""
    try:
        with open(KERNEL_PID_FILE, "w") as f:
            f.write(str(os.getpid()))
        logger.debug("Wrote PID %d to %s", os.getpid(), KERNEL_PID_FILE)
    except Exception as e:
        logger.warning("Could not write PID file %s: %s", KERNEL_PID_FILE, e)


# ---------------------------------------------------------------------------
# Original EG launcher code (unchanged unless noted)
# ---------------------------------------------------------------------------


class ExceptionThread(Thread):
    """Wrap thread to handle the exception."""

    def __init__(self, target):
        """Initialize the thread."""
        self.target = target
        self.exc = None
        Thread.__init__(self)

    def run(self):
        """Run the thread."""
        try:
            self.target()
        except Exception as exc:
            self.exc = exc


def initialize_namespace(namespace, cluster_type="spark"):
    """Initialize the kernel namespace.

    Parameters
    ----------
    cluster_type : {'spark', 'dask', 'none'}
        The cluster type to initialize. ``'none'`` results in no variables in
        the initial namespace.
    """
    if cluster_type == "spark":
        try:
            from pyspark.sql import SparkSession
        except ImportError:
            logger.info(
                "A spark context was desired but the pyspark distribution is not present.  "
                "Spark context creation will not occur."
            )
            return

        def initialize_spark_session():
            import atexit

            """Initialize Spark session and replace global variable
            placeholders with real Spark session object references."""
            spark = SparkSession.builder.getOrCreate()

            global __spark_context
            __spark_context = spark.sparkContext

            # Stop the spark session on exit
            atexit.register(lambda: spark.stop())

            namespace.update(
                {
                    "spark": spark,
                    "sc": spark.sparkContext,
                    "sql": spark.sql,
                    "sqlContext": spark._wrapped,
                    "sqlCtx": spark._wrapped,
                }
            )

        init_thread = ExceptionThread(target=initialize_spark_session)
        spark = WaitingForSparkSessionToBeInitialized("spark", init_thread, namespace)
        sc = WaitingForSparkSessionToBeInitialized("sc", init_thread, namespace)
        sqlContext = WaitingForSparkSessionToBeInitialized("sqlContext", init_thread, namespace)

        def sql(query):
            """Placeholder function. When called will wait for Spark session to be
            initialized and call ``spark.sql(query)``"""
            return spark.sql(query)

        namespace.update(
            {"spark": spark, "sc": sc, "sql": sql, "sqlContext": sqlContext, "sqlCtx": sqlContext}
        )

        init_thread.start()

    elif cluster_type == "dask":
        import dask_yarn

        cluster = dask_yarn.YarnCluster.from_current()
        namespace.update({"cluster": cluster})
    elif cluster_type != "none":
        raise RuntimeError("Unknown cluster_type: %r" % cluster_type)


class WaitingForSparkSessionToBeInitialized:
    """Wrapper object for SparkContext and other Spark session variables while the real Spark session is being
    initialized in a background thread. The class name is intentionally worded verbosely explicit as it will show up
    when executing a cell that contains only a Spark session variable like ``sc`` or ``sqlContext``.
    """

    # private and public attributes that show up for tab completion,
    # to indicate pending initialization of Spark session
    _WAITING_FOR_SPARK_SESSION_TO_BE_INITIALIZED = "Spark Session not yet initialized ..."
    WAITING_FOR_SPARK_SESSION_TO_BE_INITIALIZED = "Spark Session not yet initialized ..."

    # the same wrapper class is used for all Spark session variables, so we need to record the name of the variable
    def __init__(self, global_variable_name, init_thread, namespace):
        """Initialize the waiter."""
        self._spark_session_variable = global_variable_name
        self._init_thread = init_thread
        self._namespace = namespace

    # we intercept all method and attribute references on our temporary Spark session variable,
    # wait for the thread to complete initializing the Spark sessions and then we forward the
    # call to the real Spark objects
    def __getattr__(self, name):
        """Handle attribute getter."""
        # ignore tab-completion request for __members__ or __methods__ and ignore meta property requests
        if name.startswith("__") or name.startswith("_ipython_") or name.startswith("_repr_"):
            return
        else:
            # wait on thread to initialize the Spark session variables in global variable scope
            self._init_thread.join(timeout=None)
            exc = self._init_thread.exc
            if exc:
                msg = f"Variable: {self._spark_session_variable} was not initialized properly."
                raise RuntimeError(msg) from exc

            # now return attribute/function reference from actual Spark object
            return getattr(self._namespace[self._spark_session_variable], name)


def _validate_port_range(port_range):
    # if no argument was provided, return a range of 0
    if not port_range:
        return 0, 0

    try:
        port_ranges = port_range.split("..")
        lower_port = int(port_ranges[0])
        upper_port = int(port_ranges[1])

        port_range_size = upper_port - lower_port
        if port_range_size != 0 and port_range_size < min_port_range_size:
            msg = (
                f"Port range validation failed for range: '{port_range}'.  Range size must be at least "
                f"{min_port_range_size} as specified by env EG_MIN_PORT_RANGE_SIZE"
            )
            raise RuntimeError(msg) from None
    except ValueError as ve:
        msg = f"Port range validation failed for range: '{port_range}'.  Error was: {ve}"
        raise RuntimeError(msg) from None
    except IndexError as ie:
        msg = f"Port range validation failed for range: '{port_range}'.  Error was: {ie}"
        raise RuntimeError(msg) from None

    return lower_port, upper_port


def determine_connection_file(conn_file, kid):
    """If the directory exists, use the original file, else create a temporary file."""
    if conn_file is None or not os.path.exists(os.path.dirname(conn_file)):
        if kid is not None:
            basename = "kernel-" + kid
        else:
            basename = os.path.splitext(os.path.basename(conn_file))[0]
        fd, conn_file = tempfile.mkstemp(suffix=".json", prefix=basename + "_")
        os.close(fd)
        logger.debug(f"Using connection file '{conn_file}'.")

    return conn_file


def _encrypt(connection_info_str, public_key):
    """Encrypt the connection information using a generated AES key that is then encrypted using
    the public key passed from the server.  Both are then returned in an encoded JSON payload.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    aes_key = get_random_bytes(16)
    cipher = AES.new(aes_key, mode=AES.MODE_ECB)

    # Encrypt the connection info using the aes_key
    encrypted_connection_info = cipher.encrypt(pad(connection_info_str, 16))
    b64_connection_info = base64.b64encode(encrypted_connection_info)

    # Encrypt the aes_key using the server's public key
    imported_public_key = RSA.importKey(base64.b64decode(public_key.encode()))
    cipher = PKCS1_v1_5.new(key=imported_public_key)
    encrypted_key = base64.b64encode(cipher.encrypt(aes_key))

    # Compose the payload and Base64 encode it
    payload = {
        "version": LAUNCHER_VERSION,
        "key": encrypted_key.decode(),
        "conn_info": b64_connection_info.decode(),
    }
    b64_payload = base64.b64encode(json.dumps(payload).encode(encoding="utf-8"))
    return b64_payload


def return_connection_info(
    connection_file, response_addr, lower_port, upper_port, kernel_id, public_key
):
    """Returns the connection information corresponding to this kernel.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    response_parts = response_addr.split(":")
    if len(response_parts) != 2:
        logger.error(
            f"Invalid format for response address '{response_addr}'. Assuming 'pull' mode..."
        )
        return

    response_ip = response_parts[0]
    try:
        response_port = int(response_parts[1])
    except ValueError:
        logger.error(
            f"Invalid port component found in response address '{response_addr}'. Assuming 'pull' mode..."
        )
        return

    with open(connection_file) as fp:
        cf_json = json.load(fp)
        fp.close()

    # add process and process group ids into connection info
    pid = os.getpid()
    cf_json["pid"] = pid
    cf_json["pgid"] = os.getpgid(pid)

    # prepare socket address for handling signals
    comm_sock = prepare_comm_socket(lower_port, upper_port)
    cf_json["comm_port"] = comm_sock.getsockname()[1]
    cf_json["kernel_id"] = kernel_id

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.connect((response_ip, response_port))
        json_content = json.dumps(cf_json).encode(encoding="utf-8")
        logger.debug(f"JSON Payload '{json_content}")
        payload = _encrypt(json_content, public_key)
        logger.debug(f"Encrypted Payload '{payload}")
        s.send(payload)

    return comm_sock


def prepare_comm_socket(lower_port, upper_port):
    """Prepares the socket to which the server will send signal and shutdown requests.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    sock = _select_socket(lower_port, upper_port)
    logger.info(
        f"Signal socket bound to host: {sock.getsockname()[0]}, port: {sock.getsockname()[1]}"
    )
    sock.listen(1)
    sock.settimeout(5)
    return sock


def _select_ports(count, lower_port, upper_port):
    """Select and return n random ports that are available and adhere to the given port range, if applicable.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    ports = []
    sockets = []
    for _ in range(count):
        sock = _select_socket(lower_port, upper_port)
        ports.append(sock.getsockname()[1])
        sockets.append(sock)
    for sock in sockets:
        sock.close()
    return ports


def _select_socket(lower_port, upper_port):
    """Create and return a socket whose port is available and adheres to the given port range, if applicable.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    found_port = False
    retries = 0
    while not found_port:
        try:
            sock.bind(("0.0.0.0", _get_candidate_port(lower_port, upper_port)))  # noqa
            found_port = True
        except Exception:
            retries = retries + 1
            if retries > max_port_range_retries:
                msg = (
                    f"Failed to locate port within range {lower_port}..{upper_port} "
                    f"after {max_port_range_retries} retries!"
                )
                raise RuntimeError(msg) from None
    return sock


def _get_candidate_port(lower_port, upper_port):
    """Returns a port within the given range.  If the range is zero, the zero is returned.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    range_size = upper_port - lower_port
    if range_size == 0:
        return 0
    return random.randint(lower_port, upper_port)


def get_server_request(sock):
    """Gets a request from the server and returns the corresponding dictionary.

    This code also exists in the R kernel-launcher's server_listener.py script.
    """
    conn = None
    data = ""
    request_info = None
    try:
        conn, addr = sock.accept()
        while True:
            buffer = conn.recv(1024).decode("utf-8")
            if not buffer:  # send is complete
                request_info = json.loads(data)
                break
            data = data + buffer  # append what we received until we get no more...
    except Exception as e:
        if type(e) is not socket.timeout:
            raise e
    finally:
        if conn:
            conn.close()

    return request_info


def cancel_spark_jobs(sig, frame):
    """Cancel spark jobs."""
    if __spark_context is None:
        return
    try:
        __spark_context.cancelAllJobs()
    except Exception as e:
        if e.__class__.__name__ == "Py4JError":
            try:
                __spark_context.cancelAllJobs()
            except Exception as ex:
                print(
                    f"Error occurred while re-attempting Spark job cancellation when interrupting the kernel: {ex}"
                )
        else:
            print(
                f"Error occurred while attempting Spark job cancellation when interrupting the kernel: {e}"
            )


def server_listener(sock, parent_pid, cluster_type):
    """Waits for requests from the server and processes each when received.  Currently,
    these will be one of a sending a signal to the corresponding kernel process (signum) or
    stopping the listener and exiting the kernel (shutdown).

     This code also exists in the R kernel-launcher's server_listener.py script.
    """
    shutdown = False
    while not shutdown:
        request = get_server_request(sock)
        if request:
            signum = -1  # prevent logging poll requests since that occurs every 3 seconds
            if request.get("signum") is not None:
                signum = int(request.get("signum"))
                os.kill(parent_pid, signum)
                if signum == 2 and cluster_type == "spark":
                    os.kill(parent_pid, signal.SIGUSR2)
            if request.get("shutdown") is not None:
                shutdown = bool(request.get("shutdown"))
            if signum != 0:
                logger.info(f"server_listener got request: {request}")


def import_item(name):
    """Import and return ``bar`` given the string ``foo.bar``.
    Calling ``bar = import_item("foo.bar")`` is the functional equivalent of
    executing the code ``from foo import bar``.
    Parameters
    ----------
    name : string
      The fully qualified name of the module/package being imported.
    Returns
    -------
    mod : module object
       The module that was imported.
    """

    parts = name.rsplit(".", 1)
    if len(parts) == 2:
        # called with 'foo.bar....'
        package, obj = parts
        module = __import__(package, fromlist=[obj])
        try:
            pak = getattr(module, obj)
        except AttributeError:
            raise ImportError("No module named %s" % obj) from None
        return pak
    else:
        # called with un-dotted string
        return __import__(parts[0])


def start_ipython(
    namespace, cluster_type="spark", kernel_class_name=DEFAULT_KERNEL_CLASS_NAME, **kwargs
):
    """Start the ipython kernel."""
    from ipykernel.kernelapp import IPKernelApp

    # Capture the kernel class before removing 'import_item' from the namespace
    kernel_class = import_item(kernel_class_name)

    # create an initial list of variables to clear
    # we do this without deleting to preserve the locals so that
    # initialize_namespace isn't affected by this mutation
    to_delete = [k for k in namespace if not k.startswith("__")]

    # initialize the namespace with the proper variables
    initialize_namespace(namespace, cluster_type=cluster_type)

    # delete the extraneous variables
    for k in to_delete:
        del namespace[k]

    # Start the kernel.
    app = IPKernelApp.instance(kernel_class=kernel_class, user_ns=namespace, **kwargs)
    app.initialize([])
    app.start()

    # cleanup
    conn_file = kwargs["connection_file"]
    try:
        import os  # re-import os since it's removed during namespace manipulation during startup

        os.remove(conn_file)
    except Exception as e:
        print(f"Could not delete connection file '{conn_file}' at exit due to error: {e}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()

    parser.add_argument(
        "--response-address",
        dest="response_address",
        nargs="?",
        metavar="<ip>:<port>",
        help="Connection address (<ip>:<port>) for returning connection file",
    )
    parser.add_argument(
        "--kernel-id",
        dest="kernel_id",
        nargs="?",
        help="Indicates the id associated with the launched kernel.",
    )
    parser.add_argument(
        "--public-key",
        dest="public_key",
        nargs="?",
        help="Public key used to encrypt connection information",
    )
    parser.add_argument(
        "--port-range",
        dest="port_range",
        nargs="?",
        metavar="<lowerPort>..<upperPort>",
        help="Port range to impose for kernel ports",
    )
    parser.add_argument(
        "--spark-context-initialization-mode",
        dest="init_mode",
        nargs="?",
        help="the initialization mode of the spark context: lazy, eager or none",
    )
    parser.add_argument(
        "--cluster-type",
        dest="cluster_type",
        nargs="?",
        help="the kind of cluster to initialize: spark, dask, or none",
    )
    parser.add_argument(
        "--kernel-class-name",
        dest="kernel_class_name",
        nargs="?",
        default=DEFAULT_KERNEL_CLASS_NAME,
        help="Indicates the name of the kernel class to use.  Must be a subclass of 'ipykernel.kernelbase.Kernel'.",
    )
    # The following arguments are deprecated and will be used only if their mirroring arguments have no value.
    # This means that the default values for --spark-context-initialization-mode (none) and --cluster-type (spark)
    # will need to come from the mirrored args' default until deprecated items have been removed.
    parser.add_argument(
        "connection_file", nargs="?", help="Connection file to write connection info (deprecated)"
    )
    parser.add_argument(
        "--RemoteProcessProxy.response-address",
        dest="rpp_response_address",
        nargs="?",
        metavar="<ip>:<port>",
        help="Connection address (<ip>:<port>) for returning connection file (deprecated)",
    )
    parser.add_argument(
        "--RemoteProcessProxy.kernel-id",
        dest="rpp_kernel_id",
        nargs="?",
        help="Indicates the id associated with the launched kernel. (deprecated)",
    )
    parser.add_argument(
        "--RemoteProcessProxy.public-key",
        dest="rpp_public_key",
        nargs="?",
        help="Public key used to encrypt connection information (deprecated)",
    )
    parser.add_argument(
        "--RemoteProcessProxy.port-range",
        dest="rpp_port_range",
        nargs="?",
        metavar="<lowerPort>..<upperPort>",
        help="Port range to impose for kernel ports (deprecated)",
    )
    parser.add_argument(
        "--RemoteProcessProxy.spark-context-initialization-mode",
        dest="rpp_init_mode",
        nargs="?",
        default="none",
        help="the initialization mode of the spark context: lazy, eager or none (deprecated)",
    )
    parser.add_argument(
        "--RemoteProcessProxy.cluster-type",
        dest="rpp_cluster_type",
        nargs="?",
        default="spark",
        help="the kind of cluster to initialize: spark, dask, or none (deprecated)",
    )

    arguments = vars(parser.parse_args())
    connection_file = arguments["connection_file"]
    response_addr = arguments["response_address"] or arguments["rpp_response_address"]
    kernel_id = arguments["kernel_id"] or arguments["rpp_kernel_id"]
    public_key = arguments["public_key"] or arguments["rpp_public_key"]
    lower_port, upper_port = _validate_port_range(
        arguments["port_range"] or arguments["rpp_port_range"]
    )
    spark_init_mode = arguments["init_mode"] or arguments["rpp_init_mode"]
    cluster_type = arguments["cluster_type"] or arguments["rpp_cluster_type"]
    kernel_class_name = arguments["kernel_class_name"]
    ip = "0.0.0.0"  # noqa

    if connection_file is None and kernel_id is None:
        msg = "At least one of the parameters: 'connection_file' or '--kernel-id' must be provided!"
        raise RuntimeError(msg)

    if kernel_id is None:
        msg = "Parameter '--kernel-id' must be provided!"
        raise RuntimeError(msg)

    if public_key is None:
        msg = "Parameter '--public-key' must be provided!"
        raise RuntimeError(msg)

    # Initialize the kernel namespace for the given cluster type
    if cluster_type == "spark" and spark_init_mode == "none":
        cluster_type = "none"

    # If the connection file doesn't exist, then create it.
    if (connection_file and not os.path.isfile(connection_file)) or kernel_id is not None:
        key = str(uuid.uuid4()).encode()  # convert to bytes
        connection_file = determine_connection_file(connection_file, kernel_id)

        ports = _select_ports(5, lower_port, upper_port)

        write_connection_file(
            fname=connection_file,
            ip=ip,
            key=key,
            shell_port=ports[0],
            iopub_port=ports[1],
            stdin_port=ports[2],
            hb_port=ports[3],
            control_port=ports[4],
        )
        if response_addr:
            comm_socket = return_connection_info(
                connection_file, response_addr, lower_port, upper_port, kernel_id, public_key
            )
            if comm_socket:  # socket in use, start server listener process
                server_listener_process = Process(
                    target=server_listener,
                    args=(
                        comm_socket,
                        os.getpid(),
                        cluster_type,
                    ),
                )
                server_listener_process.start()

                # ---- Migration-aware: store params for post-restore resend ----
                _migration_params.update({
                    "connection_file": connection_file,
                    "response_addr": response_addr,
                    "kernel_id": kernel_id,
                    "public_key": public_key,
                    "comm_port": comm_socket.getsockname()[1],
                })
                _write_pid_file()
                # The handler instance is kept alive by the signal subsystem's
                # reference to its bound method, so it survives the namespace
                # cleanup performed by start_ipython(locals()).
                _reconnect_handler = _MigrationHandler(
                    _migration_params, RECONNECT_MAX_RETRIES, RECONNECT_BASE_DELAY
                )
                signal.signal(signal.SIGUSR1, _reconnect_handler.signal_handler)
                logger.info(
                    "Migration-aware reconnection enabled (SIGUSR1 handler registered, PID=%d)",
                    os.getpid(),
                )

    if cluster_type == "spark":
        signal.signal(signal.SIGUSR2, cancel_spark_jobs)

    # launch the IPython kernel instance
    start_ipython(
        locals(),
        cluster_type=cluster_type,
        connection_file=connection_file,
        ip=ip,
        kernel_class_name=kernel_class_name,
    )
